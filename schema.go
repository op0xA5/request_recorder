package main

import "encoding/json"

type Record struct {
	Method   string           `json:"method"`
	URL      string           `json:"url"`
	Time     string           `json:"time"`
	Protocol string           `json:"protocol"`
	Request  *RequestResponse `json:"request,omitempty"`
	Response *RequestResponse `json:"response,omitempty"`
}

type RequestResponse struct {
	Header                  Header          `json:"header"`
	OriginalContentEncoding string          `json:"original_content_encoding,omitempty"`
	Body                    string          `json:"body,omitempty"`
	BodyFile                string          `json:"body_file,omitempty"`
	BodyJson                json.RawMessage `json:"body_json,omitempty"`
	BodyMultiPart           []*MultiPart    `json:"body_multipart,omitempty"`
}

type MultiPart struct {
	Header      Header          `json:"header,omitempty"`
	Content     string          `json:"content,omitempty"`
	ContentFile string          `json:"content_file,omitempty"`
	ContentJson json.RawMessage `json:"content_json,omitempty"`
}

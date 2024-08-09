package main

import (
	"net/http"
	"net/textproto"
)

type Header map[string]interface{}

func (h Header) ToHttpHeader() http.Header {
	header := make(http.Header, len(h))
	for k, v := range h {
		switch v := v.(type) {
		case string:
			header[k] = []string{v}
		case []string:
			header[k] = v
		}
	}
	return header
}

func (h Header) ToMIMEHeader() textproto.MIMEHeader {
	header := make(textproto.MIMEHeader, len(h))
	for k, v := range h {
		switch v := v.(type) {
		case string:
			header.Set(k, v)
		case []string:
			header[k] = v
		}
	}
	return header
}

func (h *Header) FromHttpHeader(header http.Header) {
	if len(header) == 0 {
		return
	}

	if *h == nil {
		*h = make(Header)
	}

	for k, v := range header {
		if len(v) == 0 {
			continue
		}
		if len(v) == 1 {
			(*h)[k] = v[0]
		} else {
			(*h)[k] = v
		}
	}
}

func (h *Header) FromMIMEHeader(header textproto.MIMEHeader) {
	h.FromHttpHeader(http.Header(header))
}

func (h *Header) Get(key string) string {
	if *h == nil {
		return ""
	}
	v, ok := (*h)[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if s, ok := v.([]string); ok {
		if len(s) == 0 {
			return ""
		}
		return s[0]
	}
	return ""
}

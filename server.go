package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/urfave/cli/v2"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

func serverCmd() *cli.Command {
	return &cli.Command{
		Name:  "server",
		Usage: "Start server",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "listen",
				Aliases: []string{"l"},
				Usage:   "Listen address, default is ':8080' or ':443' for https",
				Value:   "",
			},
			&cli.BoolFlag{
				Name:     "https",
				Aliases:  []string{"H"},
				Usage:    "Start server with HTTPS",
				Category: "https",
			},
			&cli.StringFlag{
				Name:     "cert",
				Aliases:  []string{"c"},
				Usage:    "TLS certificate file, default is 'cert.pem'",
				Category: "https",
			},
			&cli.StringFlag{
				Name:     "key",
				Aliases:  []string{"k"},
				Usage:    "TLS key file, default is 'key.pem'",
				Category: "https",
			},
			&cli.StringFlag{
				Name:     "save",
				Aliases:  []string{"s"},
				Usage:    "Directory to store request files",
				Value:    "./",
				Category: "save request file",
			},
			&cli.IntFlag{
				Name:     "num",
				Aliases:  []string{"C"},
				Usage:    "Start number of requests to store",
				Value:    0,
				Category: "save request file",
			},
			&cli.IntFlag{
				Name:     "status",
				Aliases:  []string{"S"},
				Usage:    "HTTP status code to return",
				Value:    200,
				Category: "response",
			},
			&cli.StringFlag{
				Name:     "body",
				Aliases:  []string{"b"},
				Usage:    "HTTP body to return, default text is related to the status code",
				Value:    "",
				Category: "response",
			},
			&cli.StringFlag{
				Name:     "proxy",
				Aliases:  []string{"p"},
				Usage:    "Proxy request to another server",
				Category: "response",
			},
			&cli.StringFlag{
				Name:     "wwwroot",
				Aliases:  []string{"w"},
				Usage:    "Static files directory",
				Category: "response",
			},
		},
		Action: func(c *cli.Context) error {
			isTls := c.Bool("https") || (c.String("cert") != "" && c.String("key") != "")
			if isTls {
				if c.String("listen") == "" {
					_ = c.Set("listen", ":443")
				}
				if c.String("cert") == "" && c.String("key") == "" {
					_ = c.Set("cert", "cert.pem")
					_ = c.Set("key", "key.pem")
				}
				if c.String("cert") == "" {
					return cli.Exit("TLS certificate file is required", 1)
				}
				if c.String("key") == "" {
					return cli.Exit("TLS key file is required", 1)
				}
			} else {
				if c.String("listen") == "" {
					_ = c.Set("listen", ":8080")
				}
			}

			handler, err := httpHandler(c)
			if err != nil {
				return err
			}

			if isTls {
				log.Printf("Starting HTTPS server on '%s'", c.String("listen"))
				err := http.ListenAndServeTLS(c.String("listen"), c.String("cert"), c.String("key"), handler)
				if err != nil {
					log.Fatalf("failed to start server: %v", err)
				}
			} else {
				log.Printf("Starting HTTP server on '%s'", c.String("listen"))
				err := http.ListenAndServe(c.String("listen"), handler)
				if err != nil {
					log.Fatalf("failed to start server: %v", err)
				}
			}

			return nil
		},
	}
}

func httpHandler(c *cli.Context) (http.HandlerFunc, error) {
	saveDir := c.String("save")
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %s: %v", saveDir, err)
	}

	var responser http.HandlerFunc
	var err error
	if c.String("proxy") != "" {
		responser, err = proxyResponse(c.String("proxy"))
		if err != nil {
			return nil, err
		}
	} else if c.String("wwwroot") != "" {
		responser, err = staticResponse(c.String("wwwroot"))
		if err != nil {
			return nil, err
		}
	} else {
		responser = simpleResponse(c.Int("status"), c.String("body"))
	}

	num := int32(c.Int("num"))
	if num <= 0 {
		_num, err := maxFileNum(saveDir)
		if err != nil {
			return nil, err
		}
		num = int32(_num)
	}

	log.Printf("Requests save to '%s', file number start from %d", saveDir, num+1)

	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		path := r.URL.Path[1:]

		// replace invalid characters for filename
		path = strings.ReplaceAll(path, "/", "_")
		path = strings.ReplaceAll(path, "\\", "_")
		path = strings.ReplaceAll(path, ".", "_")

		requestNum := atomic.AddInt32(&num, 1)
		filename := fmt.Sprintf("%04d_%s_%s_%s.json",
			requestNum,
			now.Format("20060102_150405"),
			r.Method,
			path)
		filename = strings.ReplaceAll(filename, "__", "_")

		record := Record{
			Method:   r.Method,
			URL:      r.URL.String(),
			Time:     now.Format(time.RFC3339),
			Protocol: r.Proto,
		}

		var header Header
		header.FromHttpHeader(r.Header)
		delete(header, "Content-Encoding")
		record.Request = &RequestResponse{
			Header:                  header,
			OriginalContentEncoding: r.Header.Get("Content-Encoding"),
		}

		if r.Body != nil {
			defer r.Body.Close()

			contentType := r.Header.Get("Content-Type")
			var err error
			if isContentMultiPart(contentType) {
				record.Request.BodyMultiPart, err = readMultiPart(r, contentType, filename)
				if err != nil {
					log.Fatalf("failed to parse multipart: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte("failed to parse multipart"))
					return
				}
			} else if isContentJson(contentType) {
				record.Request.BodyJson, err = readJson(r.Body)
				if err != nil {
					log.Fatalf("failed to parse json: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte("failed to parse json"))
					return
				}
			} else {
				recommendFilename := fmt.Sprintf("%s-body.dat", strings.TrimSuffix(filename, ".json"))
				record.Request.Body, record.Request.BodyFile, err = saveBody(r.Body, contentType, recommendFilename)
				if err != nil {
					log.Fatalf("failed to save body: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("failed to save body"))
					return
				}
			}
		}

		// save record to file
		if err := saveRecord(saveDir, filename, &record); err != nil {
			log.Fatalf("failed to create file '%s': %v", filename, err)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("failed to create file"))
			return
		}

		responser(w, r)

		log.Printf("#%04d [%s] %s %s", requestNum, now.Format("15:04:05"), r.Method, r.URL.Path)
	}, nil
}

func simpleResponse(status int, msg string) http.HandlerFunc {
	if msg == "" {
		msg = http.StatusText(status)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(msg))
	}
}

func staticResponse(wwwroot string) (http.HandlerFunc, error) {
	fi, err := os.Stat(wwwroot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("directory %s not exists", wwwroot)
		}
		return nil, fmt.Errorf("failed to stat directory %s: %v", wwwroot, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", wwwroot)
	}

	return http.FileServerFS(os.DirFS(wwwroot)).ServeHTTP, nil
}

func proxyResponse(_url string) (http.HandlerFunc, error) {
	proxyURL, err := url.Parse(_url)
	if err != nil {
		return nil, fmt.Errorf("failed to parse proxy url %s: %v", _url, err)
	}

	_ = proxyURL

	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: proxy request
	}, nil
}

func maxFileNum(dir string) (int, error) {
	f, err := os.Open(dir)
	if err != nil {
		return 0, fmt.Errorf("failed to open directory %s: %v", dir, err)
	}
	defer f.Close()

	maxInt := 1
	for {
		dirs, err := f.ReadDir(16)
		if err != nil {
			if err == io.EOF {
				break
			}
		}

		for _, d := range dirs {
			if d.IsDir() {
				continue
			}

			name := d.Name()
			pos := strings.IndexFunc(name, func(r rune) bool {
				return r < '0' || r > '9'
			})
			if pos == -1 {
				continue
			}

			n, err := strconv.Atoi(name[:pos])
			if err != nil {
				continue
			}

			if n > maxInt {
				maxInt = n
			}
		}
	}

	return maxInt, nil
}

func isContentMultiPart(contentType string) bool {
	return strings.Contains(contentType, "multipart/form-data")
}

func readMultiPart(r *http.Request, contentType string, jsonFilename string) ([]*MultiPart, error) {
	if r.Body == nil {
		return nil, errors.New("missing form body")
	}

	var multiParts []*MultiPart
	var n int

	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("failed to create multipart reader: %w", err)
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		multiPart := &MultiPart{}
		multiPart.Header.FromMIMEHeader(p.Header)

		contentType := p.Header.Get("Content-Type")
		if isContentJson(contentType) {
			multiPart.ContentJson, err = readJson(p)
			if err != nil {
				return nil, fmt.Errorf("failed to parse json: %w", err)
			}
		} else {
			var recommendFilename string
			if p.FileName() != "" {
				recommendFilename = fmt.Sprintf("%s-%s",
					strings.TrimSuffix(jsonFilename, ".json"),
					p.FileName())
			} else {
				recommendFilename = fmt.Sprintf("%s-multipart_%d.dat",
					strings.TrimSuffix(jsonFilename, ".json"),
					n)
				n++
			}

			multiPart.Content, multiPart.ContentFile, err = saveBody(p, p.Header.Get("Content-Type"), recommendFilename)
			if err != nil {
				return nil, err
			}
		}

		multiParts = append(multiParts, multiPart)
	}

	return multiParts, nil
}

func isContentJson(contentType string) bool {
	return strings.Contains(contentType, "/json")
}

func readJson(r io.Reader) (json.RawMessage, error) {
	lr := io.LimitReader(r, 1<<20)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}

	if !json.Valid(data) {
		return nil, errors.New("invalid json")
	}
	return data, nil
}

func saveBody(r io.Reader, contentType string, recommendFilename string) (string, string, error) {
	var buffer []byte

	buffer = make([]byte, 64*1024)
	hasMore := true
	n, err := io.ReadFull(r, buffer)
	if err != nil {
		if err == io.EOF {
			return "", "", nil
		}
		if err == io.ErrUnexpectedEOF {
			buffer = buffer[:n]
			hasMore = false
		} else {
			return "", "", err
		}
	}

	if hasMore {
		goto saveFile
	}
	for _, r := range string(buffer) {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r)) {
			goto saveFile
		}
	}

	return string(buffer), "", nil

saveFile:
	ext, _ := mime.ExtensionsByType(contentType)
	if len(ext) >= 0 {
		recommendFilename = strings.TrimSuffix(recommendFilename, filepath.Ext(recommendFilename))
		recommendFilename = fmt.Sprintf("%s%s", recommendFilename, ext[0])
	}

	f, err := os.Create(recommendFilename)
	if err != nil {
		return "", "", err
	}

	defer f.Close()

	if len(buffer) > 0 {
		if _, err := f.Write(buffer); err != nil {
			return "", "", err
		}
	}
	if _, err := io.Copy(f, r); err != nil {
		return "", "", err
	}

	return "", recommendFilename, nil
}

func saveRecord(dir, filename string, record *Record) error {
	f, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(record); err != nil {
		return err
	}
	return nil
}

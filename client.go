package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/urfave/cli/v2"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func clientCmd() *cli.Command {
	return &cli.Command{
		Name:  "req",
		Usage: "Replay a request",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "file",
				Aliases:  []string{"f"},
				Usage:    "Request JSON file",
				Required: true,
			},
			&cli.StringFlag{
				Name:    "server",
				Aliases: []string{"s"},
				Usage:   "Server address, if a url is provided, it will override the url from request file",
				Value:   "localhost",
			},
			&cli.IntFlag{
				Name:    "port",
				Aliases: []string{"p"},
				Usage:   "Server port, default is 80 or 443 for https",
			},
			&cli.BoolFlag{
				Name:    "https",
				Aliases: []string{"H"},
				Usage:   "Use HTTPS",
			},
			&cli.BoolFlag{
				Name:  "insecure",
				Usage: "Skip SSL verification",
			},
			&cli.StringFlag{
				Name:  "basic",
				Usage: "Use basic authentication",
			},
			&cli.StringFlag{
				Name:  "bearer",
				Usage: "Use bearer token",
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Verbose output",
			},
		},
		Action: func(c *cli.Context) error {
			uri, err := parseUri(c)
			if err != nil {
				return err
			}

			var record Record
			if err := loadRecord(c.String("file"), &record); err != nil {
				return err
			}

			if uri.Path == "" {
				uri.Path = record.URL
			}

			req := &http.Request{}
			req.URL = uri
			req.Method = record.Method
			req.Proto = record.Protocol
			req.Header = record.Request.Header.ToHttpHeader()
			req.Body, err = parseRecordBody(record.Request, req.Header, filepath.Base(c.String("file")))
			if err != nil {
				return err
			}

			if c.String("basic") != "" {
				req.Header.Set("Authorization",
					"Basic "+base64.StdEncoding.EncodeToString([]byte(c.String("basic"))))
			}
			if c.String("bearer") != "" {
				req.Header.Set("Authorization", "Bearer "+c.String("bearer"))
			}

			client := &http.Client{
				Transport: http.DefaultTransport,
			}
			defer client.CloseIdleConnections()
			if c.Bool("insecure") {
				client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			}
			if c.Bool("verbose") {
				client.Transport.(*http.Transport).DialContext = verboseDial(false, false)
				client.Transport.(*http.Transport).DialTLSContext = verboseDial(true, c.Bool("insecure"))
			}

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("failed to send request: %s", err)
			}

			defer resp.Body.Close()
			log.Printf("Response: %s\n", resp.Status)

			if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
				return fmt.Errorf("failed to print out response body")
			}

			return nil
		},
	}
}

func parseUri(c *cli.Context) (*url.URL, error) {
	var uri *url.URL
	var err error

	if strings.Contains(c.String("server"), "://") {
		uri, err = url.Parse(c.String("server"))
		if err != nil {
			return nil, fmt.Errorf("failed to parse server url: %s", err)
		}
	} else {
		serverSp := strings.SplitN(c.String("server"), "/", 2)
		host, port, err := net.SplitHostPort(serverSp[0])
		if err != nil {
			var addErr *net.AddrError
			if errors.As(err, &addErr) {
				if addErr.Err == "missing port in address" {
					err = nil
					host = serverSp[0]
					_port := c.Int("port")
					if _port > 0 {
						_defaultPort := 80
						if c.Bool("https") {
							_defaultPort = 443
						}
						if _port != _defaultPort {
							port = strconv.Itoa(c.Int("port"))
						}
					}
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("failed parse param server: %s", err)
		}

		uri = &url.URL{}
		if port == "" {
			uri.Host = host
		} else {
			uri.Host = net.JoinHostPort(host, port)
		}
		if len(serverSp) > 1 {
			uri.Path = "/" + serverSp[1]
		}
	}

	if uri.Scheme == "" {
		if c.Bool("https") {
			uri.Scheme = "https"
		} else {
			uri.Scheme = "http"
		}
	}

	return uri, nil
}

func loadRecord(filename string, record *Record) error {
	f, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open file: %s", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	if err := dec.Decode(record); err != nil {
		return fmt.Errorf("failed to decode json: %s", err)
	}
	return nil
}

func parseRecordBody(req *RequestResponse, header http.Header, baseDir string) (io.ReadCloser, error) {
	if req.BodyFile != "" {
		if header.Get("Content-Type") == "" {
			header.Set("Content-Type", mime.TypeByExtension(filepath.Ext(req.BodyFile)))
		}

		return os.Open(filepath.Join(baseDir, req.BodyFile))
	}
	if req.BodyJson != nil {
		if header.Get("Content-Type") == "" {
			header.Set("Content-Type", "application/json")
		}

		buffer := &bytes.Buffer{}
		err := json.Compact(buffer, req.BodyJson)
		if err != nil {
			return nil, fmt.Errorf("failed to parse json: %s", err)
		}
		return io.NopCloser(buffer), nil
	}
	if req.BodyMultiPart != nil {
		pReader, pWriter := io.Pipe()

		mw := multipart.NewWriter(pWriter)
		contentType := header.Get("Content-Type")
		if contentType != "" {
			_, params, _ := mime.ParseMediaType(contentType)
			boundary, _ := params["boundary"]
			if boundary != "" {
				_ = mw.SetBoundary(boundary)
			}
		} else {
			header.Set("Content-Type", mw.FormDataContentType())
		}

		go func() (_err error) {
			defer pWriter.CloseWithError(_err)
			defer mw.Close()

			for _, part := range req.BodyMultiPart {
				partWriter, err := mw.CreatePart(part.Header.ToMIMEHeader())
				if err != nil {
					return err
				}
				if part.ContentFile != "" {
					file, err := os.Open(filepath.Join(baseDir, part.ContentFile))
					if err != nil {
						return err
					}
					defer file.Close()

					if _, err := io.Copy(partWriter, file); err != nil {
						return err
					}
				} else if part.ContentJson != nil {
					if _, err := partWriter.Write(part.ContentJson); err != nil {
						return err
					}
				} else {
					if _, err := partWriter.Write([]byte(part.Content)); err != nil {
						return err
					}
				}
			}

			return nil
		}()

		return pReader, nil
	}

	if header.Get("Content-Type") == "" {
		header.Set("Content-Type", "text/plain")
	}
	return io.NopCloser(strings.NewReader(req.Body)), nil
}

func verboseDial(dialTLS bool, insecure bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		log.Printf("-- Connecting to server '%s' ...", addr)
		dialer := &net.Dialer{}
		rawConn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			log.Printf("   Connect failed: %s", err)
		} else {
			log.Printf("   Connected")
		}

		if !dialTLS {
			return rawConn, err
		}

		colonPos := strings.LastIndex(addr, ":")
		if colonPos == -1 {
			colonPos = len(addr)
		}
		hostname := addr[:colonPos]

		conn := tls.Client(rawConn, &tls.Config{
			ServerName:         hostname,
			InsecureSkipVerify: insecure,
		})

		log.Printf("-- TLS handshake ...")
		if err := conn.HandshakeContext(ctx); err != nil {
			rawConn.Close()

			log.Printf("   Handshake failed: %s", err)
			return nil, err
		}

		log.Printf("   Handshake OK")
		return conn, nil
	}
}

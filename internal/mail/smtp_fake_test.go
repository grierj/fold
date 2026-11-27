package mail

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

type fake struct{ port int }

// fakeSMTP is a minimal SMTP server on loopback that never offers STARTTLS.
func fakeSMTP(t *testing.T, _ bool) fake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				w := func(s string) { c.Write([]byte(s + "\r\n")) }
				w("220 fake")
				data := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if data {
						if line == ".\r\n" {
							data = false
							w("250 ok")
						}
						continue
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						w("250 fake")
					case cmd == "DATA":
						data = true
						w("354 go")
					case cmd == "QUIT":
						w("221 bye")
						return
					default:
						w("250 ok")
					}
				}
			}(c)
		}
	}()
	return fake{port: ln.Addr().(*net.TCPAddr).Port}
}

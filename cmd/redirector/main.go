// Command redirector: Hot-path redirector only (role redirector); listens on REDIRECTOR_PORT.
package main

import "github.com/0mr4n/Tusk/cmd/internal/app"

func main() { app.Main("redirector", []string{"redirector"}) }

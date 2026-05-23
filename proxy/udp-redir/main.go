// udp-redir listens on NFQUEUE 0, parses outbound UDP packets emitted
// from the Claude container's processes, consults the proxy rule store,
// and verdicts each packet ACCEPT / DROP / hold-for-resolve.
package main

import (
	"log"

	_ "github.com/florianl/go-nfqueue"
)

func main() {
	log.Println("udp-redir: starting")
}

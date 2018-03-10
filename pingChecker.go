// sudo sysctl -w net.ipv4.ping_group_range="0 65535"
package main

import (
    "time"
    "github.com/tatsushid/go-fastping"
    "net"
    "fmt"
    "os"
    "bufio"
    "log"
    "net/http"
    "database/sql"
    _ "github.com/go-sql-driver/mysql"
)

const (
    numPollers     = 348              // number of Poller goroutines to launch
    pollInterval   = 10 * time.Second // how often to poll each URL
    statusInterval = 10 * time.Second // how often to log status to stdout
    errTimeout     = 10 * time.Second // back-off timeout on error
)

var ipList = []string{}
var db *sql.DB // global variable to share it between main and the HTTP handler

// State represents the last-known state of a URL.
type State struct {
    ipAdderess string
    state      int
}

// StateMonitor maintains a map that stores the state of the URLs being
// polled, and prints the current state every updateInterval nanoseconds.
// It returns a chan State to which resource state should be sent.
func StateMonitor(updateInterval time.Duration) chan<- State {
    updates := make(chan State)
    urlStatus := make(map[string]int)
    ticker := time.NewTicker(updateInterval)
    go func() {
        for {
            select {
            case <-ticker.C:
                logState(urlStatus)
            case s := <-updates:
                if _, ok := urlStatus[s.ipAdderess]; ok {
                    if s.state == 0 && urlStatus[s.ipAdderess] > 0 {
                        if urlStatus[s.ipAdderess] == 6 {
                            hostStateChange(s.ipAdderess, 1)
                        }
                        urlStatus[s.ipAdderess] = 0
                    }
                    if s.state == 1 {
                        if urlStatus[s.ipAdderess] == 5 {
                            urlStatus[s.ipAdderess]++
                            hostStateChange(s.ipAdderess, 0)
                        }
                        if urlStatus[s.ipAdderess] == 4 || urlStatus[s.ipAdderess] == 3 || urlStatus[s.ipAdderess] == 2 || urlStatus[s.ipAdderess] == 1 || urlStatus[s.ipAdderess] == 0 {
                            urlStatus[s.ipAdderess]++
                        }
                    }
                } else {
                    urlStatus[s.ipAdderess] = s.state
                }
            }
        }
    }()
    return updates
}

func hostStateChange(ipAdderess string, state int) {
    var sendedState string
    if state == 0 {
        sendedState = "сдох"
        db.Query("INSERT INTO pingChecker SET `when`=now(), state=0, ip=?", ipAdderess)
    } else {
        sendedState = "ожил"
        db.Query("INSERT INTO pingChecker SET `when`=now(), state=1, ip=?", ipAdderess)
    }
    http.Get("https://krasit.org/tSend.php?m=" + ipAdderess + "%20" + sendedState)
    log.Printf("%s %s", sendedState, ipAdderess)
}

// logState prints a state map.
func logState(s map[string]int) {
    //log.Println("Current state:")
    //for k, v := range s {
    //	log.Printf(" %s %d", k, v)
    //}
}

// Resource represents an HTTP URL to be polled by this program.
type Resource struct {
    ipAdderess string
    state      int
}

// Poll executes an HTTP HEAD request for url
// and returns the HTTP status string or an error string.
func (r *Resource) Poll() int {
    pingResult := 0
    p := fastping.NewPinger()
    _, err := p.Network("udp")

    p.AddIP(r.ipAdderess)
    p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
        pingResult = 1
    }
    err = p.Run()
    if err != nil {
        fmt.Println(err)
    }
    if pingResult == 1 {
        return 0
    } else {
        return 1
    }
}

// Sleep sleeps for an appropriate interval (dependent on error state)
// before sending the Resource to done.
func (r *Resource) Sleep(done chan<- *Resource) {
    time.Sleep(pollInterval + errTimeout*time.Duration(r.state))
    done <- r
}

func Poller(in <-chan *Resource, out chan<- *Resource, status chan<- State) {
    for r := range in {
        s := r.Poll()
        status <- State{r.ipAdderess, s}
        out <- r
    }
}

func readLine(path string) {
    inFile, _ := os.Open(path)
    defer inFile.Close()
    scanner := bufio.NewScanner(inFile)
    scanner.Split(bufio.ScanLines)

    for scanner.Scan() {
        ipList = append(ipList, scanner.Text())
    }
}

func main() {
    var err error
    db, err = sql.Open("mysql", os.Args[1]+":"+os.Args[2]+"@tcp("+os.Args[3]+")/"+os.Args[4]+"") // this does not really open a new connection

    if err != nil {
        log.Fatalf("Error on initializing database connection: %s", err.Error())
    }
    db.SetMaxIdleConns(5)
    err = db.Ping() // This DOES open a connection if necessary. This makes sure the database is accessible
    if err != nil {
        log.Fatalf("Error on opening database connection: %s", err.Error())
    }

    readLine("hosts.txt")
    // Create our input and output channels.
    pending, complete := make(chan *Resource), make(chan *Resource)

    // Launch the StateMonitor.
    status := StateMonitor(statusInterval)

    // Launch some Poller goroutines.
    for i := 0; i < numPollers; i++ {
        go Poller(pending, complete, status)
    }

    // Send some Resources to the pending queue.
    go func() {
        for _, ipAddress := range ipList {
            pending <- &Resource{ipAdderess: ipAddress}
        }
    }()

    for r := range complete {
        go r.Sleep(pending)
    }
}

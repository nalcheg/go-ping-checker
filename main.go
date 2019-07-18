package main

import (
	"database/sql"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/tatsushid/go-fastping"
)

type State struct {
	IpAddress  string    `db:"ip"`
	State      uint8     `db:"state"`
	UpdatedAt  time.Time `db:"when"`
	FailsCount uint8
	WaitGroup  *sync.WaitGroup
}

var maxFailsCount uint8 = 6
var workersCount uint64 = 10

func main() {
	if os.Getenv("ALLOWED_FAILS") != "" {
		maxFails, err := strconv.ParseUint(os.Getenv("ALLOWED_FAILS"), 10, 32)
		if err != nil {
			log.Print(err)
		}
		maxFailsCount = uint8(maxFails)
	}

	go func() {
		for {
			if runtime.NumGoroutine() > 250 {
				log.Print(runtime.NumGoroutine())
				panic("panic")
			}
			time.Sleep(time.Second)
		}
	}()

	db, err := sql.Open("mysql", os.Getenv("DB_DSN"))
	if err != nil {
		log.Fatalf("Error on initializing database connection: %s", err.Error())
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("Error on opening database connection: %s", err.Error())
	}
	db.SetMaxIdleConns(3)
	db.SetMaxOpenConns(6)

	tableName := os.Getenv("TABLE_NAME")
	statesMap := initAddresses(db, tableName)

	input, output := make(chan State), make(chan State)

	if os.Getenv("WORKERS_COUNT") != "" {
		workersCount, err = strconv.ParseUint(os.Getenv("ALLOWED_FAILS"), 10, 32)
		if err != nil {
			log.Print(err)
		}
	}

	for i := uint64(0); i < workersCount; i++ {
		go pinger(input, output)
	}

	go func() {
		for {
			select {
			case newValue := <-output:
				if value, ok := statesMap.Load(newValue.IpAddress); ok != false {
					oldValue := value.(State)
					if oldValue.FailsCount > 0 && newValue.State == 1 {
						newValue.FailsCount = 0
						log.Printf("%s reset with %d fails", newValue.IpAddress, oldValue.FailsCount)
					}
					if oldValue.State != newValue.State {
						if newValue.State == 1 {
							newValue.FailsCount = 0
							newValue.State = 1
							if _, err := db.Exec(
								"INSERT INTO "+tableName+" SET `when`=NOW(), state=?, ip=?",
								newValue.State,
								newValue.IpAddress,
							); err != nil {
								log.Fatal(err)
							}

							log.Printf("%s up", newValue.IpAddress)
						} else if newValue.FailsCount >= maxFailsCount-1 {
							newValue.FailsCount = 0
							newValue.State = 0
							if _, err := db.Exec(
								"INSERT INTO "+tableName+" SET `when`=NOW(), state=?, ip=?",
								newValue.State,
								newValue.IpAddress,
							); err != nil {
								log.Fatal(err)
							}

							log.Printf("%s down", newValue.IpAddress)
						} else {
							newValue.FailsCount++
							newValue.State = oldValue.State
							log.Printf("%s going to fall fails count %d", newValue.IpAddress, newValue.FailsCount)
						}
					}

					statesMap.Store(newValue.IpAddress, newValue)
				}

				newValue.WaitGroup.Done()
			}
		}
	}()

	var wg sync.WaitGroup
	for {
		log.Print("start round")
		statesMap.Range(func(key, value interface{}) bool {
			wg.Add(1)
			if value, ok := value.(State); ok == true {
				value.WaitGroup = &wg
				input <- value

				return true
			}

			return false
		})

		wg.Wait()
	}
}

func initAddresses(db *sql.DB, tableName string) *sync.Map {
	rows, err := db.Query("SELECT ip,state FROM " + tableName + " f WHERE id = (SELECT id FROM " + tableName + " WHERE ip=f.ip ORDER BY id DESC LIMIT 1)")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Fatal(err)
		}
	}()

	var statesMap sync.Map
	var pingCheckRow State
	i := 0
	for rows.Next() {
		if err := rows.Scan(&pingCheckRow.IpAddress, &pingCheckRow.State); err != nil {
			log.Fatal(err)
		}
		statesMap.Store(pingCheckRow.IpAddress, State{
			IpAddress:  pingCheckRow.IpAddress,
			State:      pingCheckRow.State,
			UpdatedAt:  time.Now(),
			FailsCount: 0,
		})
		i++
	}
	log.Printf("loaded %d addresses", i)

	return &statesMap
}

func pinger(in <-chan State, out chan<- State) {
	for {
		select {
		case work := <-in:
			pingResult := uint8(0)
			p := fastping.NewPinger()

			if os.Getenv("UDP_PING") == "true" {
				if _, err := p.Network("udp"); err != nil {
					log.Print(err)
				}
			}

			if err := p.AddIP(work.IpAddress); err != nil {
				log.Print(err)
			}

			p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
				pingResult = 1
			}

			if err := p.Run(); err != nil {
				log.Print(err)
			}

			work.UpdatedAt = time.Now()
			work.State = pingResult

			out <- work
		}
	}
}

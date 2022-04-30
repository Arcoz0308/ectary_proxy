package main

import (
	"flag"
	"fmt"
	"github.com/paroxity/portal/config"
	"github.com/paroxity/portal/event"
	"github.com/paroxity/portal/logger"
	"github.com/paroxity/portal/query"
	"github.com/paroxity/portal/server"
	"github.com/paroxity/portal/session"
	"github.com/paroxity/portal/socket"
	"github.com/sandertv/gophertunnel/minecraft"
	packet2 "github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/text"
	"github.com/sirupsen/logrus"
	"gopkg.in/square/go-jose.v2/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var otherPlayers int = 0
var otherServers []string
var webPort string
var loadBalancer []string

type sHandler struct {
}

func main() {

	f, err := os.Open("web.json")
	if err != nil {
		panic(err)
	}

	var c = &struct {
		OtherServers []string `json:"other_servers"`
		Port         string   `json:"port"`
		LoadBalancer []string `load_balancer`
	}{}

	if err = json.NewDecoder(f).Decode(c); err != nil {
		panic(err)
	}

	otherServers = c.OtherServers
	webPort = c.Port
	loadBalancer = c.LoadBalancer

	var configPath string
	flag.StringVar(&configPath, "config", "config.json", "Path to the config file.")
	flag.Parse()

	if err = config.Load(configPath); err != nil {
		fmt.Printf("Unable to load config: %v", err)
	}
	log, err := logger.New(config.LogFile())
	if err != nil {
		panic(err)
	}
	logrus.SetFormatter(&logrus.TextFormatter{
		ForceColors:     true,
		FullTimestamp:   true,
		TimestampFormat: "15:04:05",
	})
	logrus.SetOutput(log)
	logrus.SetLevel(config.LogLevel())

	l, err := minecraft.ListenConfig{
		AuthenticationDisabled: !config.Authentication(),
		StatusProvider:         statusProvider{},
		ResourcePacks:          config.ResourcePacks(),
		TexturePacksRequired:   config.ForceTexturePacks(),
	}.Listen("raknet", config.BindAddress())
	if err != nil {
		logrus.Fatalf("Unable to start listener: %v\n", err)
	}
	logrus.Infof("Listening on %s\n", config.BindAddress())

	go func() {
		if err := socket.Listen(); err != nil {
			panic(err)
		}
	}()

	go func() {
		LoadOtherProxy()
		ticker := time.NewTicker(time.Second * 30)
		quit := make(chan struct{})
		go func() {
			for {
				select {
				case <-ticker.C:
					LoadOtherProxy()
				case <-quit:
					ticker.Stop()
					return
				}
			}
		}()
	}()
	go listenWeb()
	if config.ReportPlayerLatency() {
		go socket.ReportPlayerLatency(config.PlayerLatencyUpdateInterval())
	}

	for {
		conn, err := l.Accept()
		if err != nil {
			logrus.Infof("Unable to accept connection: %v\n", err)
			return
		}

		go handleConnection(l, conn.(*minecraft.Conn))
	}
}

// handleConnection handles an incoming connection from the Listener.
func handleConnection(l *minecraft.Listener, conn *minecraft.Conn) {
	var whitelisted bool
	for _, p := range config.Whitelisted() {
		if strings.EqualFold(conn.IdentityData().DisplayName, p) {
			whitelisted = true
			break
		}
	}
	if config.Whitelist() && !whitelisted {
		_ = l.Disconnect(conn, text.Colourf("<red>Server is whitelisted</red>"))
		logrus.Infof("%s failed to join: Server is whitelisted\n", conn.IdentityData().DisplayName)
		return
	}

	s, err := session.New(conn)
	s.Handle(sHandler{})
	if err != nil {
		logrus.Errorf("Unable to create session, %v\n", err)
		_ = l.Disconnect(conn, text.Colourf("<red>%v</red>", err))
		return
	}
	logrus.Infof("%s has been connected to server %s in group %s\n", s.Conn().IdentityData().DisplayName, s.Server().Name(), s.Server().Group())
}

type statusProvider struct{}

// ServerStatus ...
func (s statusProvider) ServerStatus(_, _ int) minecraft.ServerStatus {
	return minecraft.ServerStatus{
		ServerName:  config.MOTD(),
		PlayerCount: query.PlayerCount() + otherPlayers,
		MaxPlayers:  config.MaxPlayers(),
	}
}

type handler struct{}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.ToLower(r.URL.Path) == "/full" || strings.ToLower(r.URL.Path) == "/full/" {
		s := strconv.Itoa(query.PlayerCount() + otherPlayers)
		w.Write([]byte(s))
		return
	}
	s := strconv.Itoa(query.PlayerCount())
	w.Write([]byte(s))
}

func listenWeb() {
	server := &http.Server{
		Addr:    webPort,
		Handler: handler{},
	}
	log.Fatal(server.ListenAndServe())
}

func LoadOtherProxy() {
	var i2 int64 = 0
	var i *int64 = &i2
	wg := &sync.WaitGroup{}
	wg.Add(len(otherServers))
	for _, server := range otherServers {
		go recServ(i, server, wg)
		continue
	}
	wg.Wait()
	otherPlayers = int(*i)
}
func recServ(i *int64, server string, wg *sync.WaitGroup) {
	c := http.Client{Timeout: time.Second * 3}
	r, err := c.Get(server)
	if err != nil {
		logrus.Errorln("failed to get "+server+", error :", err)
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		logrus.Errorln("failed to read byte of "+server+", error :", err)
	}
	n, err := strconv.Atoi(string(b))
	if err != nil {
		logrus.Errorln("failed to read byte of "+server+", error :", err)
	}
	atomic.AddInt64(i, int64(n))
	wg.Done()
}

func (sHandler) HandleClientBoundPacket(ctx *event.Context, pk packet2.Packet) {}

// HandleServerBoundPacket ...
func (sHandler) HandleServerBoundPacket(*event.Context, packet2.Packet) {}

// HandleServerDisconnect ...
func (sHandler) HandleServerDisconnect(*event.Context) {}

// HandleTransfer ...
func (sHandler) HandleTransfer(ctx *event.Context, s *server.Server) {
	for _, lb := range loadBalancer {
		if lb == s.Name() {
			group, _ := server.GroupFromName(s.Group())
			srv := choseServ(group.Servers())

			if srv == nil {
				ctx.Cancel()
				return
			}
			s = srv
		}
	}
}

// HandleQuit ...
func (sHandler) HandleQuit() {}

func choseServ(servers map[string]*server.Server) (serv *server.Server) {
	var onlineServers []*server.Server
	for _, s := range servers {
		if s.Connected() {
			onlineServers = append(onlineServers, s)
		}
	}
	if len(onlineServers) == 0 {
		return
	}
	if len(onlineServers) == 1 {
		return onlineServers[0]
	}
	// all servers with 12 online players or more
	var servers2 []*server.Server
	for _, s := range onlineServers {
		if s.PlayerCount() >= 12 {
			servers2 = append(servers2, s)
		}
	}
	if len(servers2) == 0 {
		for _, s := range onlineServers {
			if serv == nil || serv.PlayerCount() < s.PlayerCount() {
				serv = s
			}
		}
	} else if len(servers2) < len(onlineServers) {
		for _, s := range onlineServers {
			if (serv == nil || serv.PlayerCount() < s.PlayerCount()) && s.PlayerCount() < 12 {
				serv = s
			}
		}
	} else {
		for _, s := range servers2 {
			if serv == nil || serv.PlayerCount() > s.PlayerCount() {
				serv = s
			}
		}
	}
	return
}

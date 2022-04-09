package main

import (
	"flag"
	"fmt"
	"github.com/paroxity/portal/config"
	"github.com/paroxity/portal/logger"
	"github.com/paroxity/portal/query"
	"github.com/paroxity/portal/session"
	"github.com/paroxity/portal/socket"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/text"
	"github.com/sirupsen/logrus"
	"gopkg.in/square/go-jose.v2/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

var otherPlayers int = 0
var otherServers []string
var webPort string

func main() {

	f, err := os.Open("web.json")
	if err != nil {
		panic(err)
	}

	var c = &struct {
		OtherServers []string `json:"other_servers"`
		Port         string   `json:"port"`
	}{}

	if err = json.NewDecoder(f).Decode(c); err != nil {
		panic(err)
	}

	otherServers = c.OtherServers
	webPort = c.Port

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

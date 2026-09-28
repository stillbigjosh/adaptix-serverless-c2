package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strings"

	adaptix "github.com/Adaptix-Framework/axc2"
)

type Teamserver interface {
	TsAgentIsExists(agentId string) bool
	TsAgentCreate(agentCrc string, agentId string, beat []byte, listenerName string, ExternalIP string, Async bool) (adaptix.AgentData, error)
	TsAgentProcessData(agentId string, bodyData []byte) error
	TsAgentSetTick(agentId string, listenerName string) error
	TsAgentGetHostedAll(agentId string, maxDataSize int) ([]byte, error)
	TsExtenderDataSave(extenderName string, key string, value []byte) error
	TsExtenderDataLoad(extenderName string, key string) ([]byte, error)
	TsExtenderDataDelete(extenderName string, key string) error
	TsExtenderDataKeys(extenderName string) ([]string, error)
}

type PluginListener struct{}

var (
	ModuleDir       string
	ListenerDataDir string
	Ts              Teamserver
)

func InitPlugin(ts any, moduleDir string, listenerDir string) adaptix.PluginListener {
	ModuleDir = moduleDir
	ListenerDataDir = listenerDir
	Ts = ts.(Teamserver)
	return &PluginListener{}
}

type ServerlessConfig struct {
	Region        string `json:"region"`
	LambdaURL     string `json:"lambda_url"`
	RelayAPIKey   string `json:"relay_api_key"`
	UploadedFile  string `json:"uploaded_file"`
	Ssl           bool   `json:"ssl"`
	InboundTable  string `json:"inbound_table"`
	OutboundTable string `json:"outbound_table"`
	AgentsTable   string `json:"agents_table"`
	PollInterval  int    `json:"poll_interval"`
	EncryptKey    string `json:"encrypt_key"`
	TTLHours      int    `json:"ttl_hours"`
	Protocol      string `json:"protocol"`
}

type Listener struct {
	name      string
	config    ServerlessConfig
	transport *TransportServerless
	active    bool
}

func generateEncryptKey() string {
	key := make([]byte, 16)
	rand.Read(key)
	return hex.EncodeToString(key)
}

func (p *PluginListener) Create(name string, config string, customData []byte) (adaptix.ExtenderListener, adaptix.ListenerData, []byte, error) {
	var (
		listener     *Listener
		listenerData adaptix.ListenerData
		conf         ServerlessConfig
		customdData  []byte
		err          error
	)

	if customData == nil {
		if err = json.Unmarshal([]byte(config), &conf); err != nil {
			return nil, listenerData, customdData, err
		}
		conf.Protocol = "serverless"
		conf.Ssl = true
		if conf.Region == "" {
			conf.Region = "us-east-1"
		}
		if conf.InboundTable == "" {
			conf.InboundTable = "adaptix-inbound"
		}
		if conf.OutboundTable == "" {
			conf.OutboundTable = "adaptix-outbound"
		}
		if conf.AgentsTable == "" {
			conf.AgentsTable = "adaptix-agents"
		}
		if conf.PollInterval <= 0 {
			conf.PollInterval = 5
		}
		if conf.TTLHours <= 0 {
			conf.TTLHours = 24
		}
		if conf.EncryptKey == "" {
			conf.EncryptKey = generateEncryptKey()
		}
	} else {
		if err = json.Unmarshal(customData, &conf); err != nil {
			return nil, listenerData, customdData, err
		}
	}

	if conf.UploadedFile != "" && conf.LambdaURL != "" {
		conf.UploadedFile = patchProfileHost(conf.UploadedFile, conf.LambdaURL)
	}

	transport := NewTransportServerless(name, conf)

	listener = &Listener{
		name:      name,
		config:    conf,
		transport: transport,
		active:    false,
	}

	listenerData = adaptix.ListenerData{
		BindHost:  "lambda",
		BindPort:  conf.Region,
		AgentAddr: conf.LambdaURL,
		Status:    "Stopped",
	}
	customdData, err = json.Marshal(conf)
	if err != nil {
		return nil, listenerData, customdData, err
	}

	return listener, listenerData, customdData, nil
}

func (l *Listener) Start() error {
	l.active = true
	return l.transport.Start()
}

func (l *Listener) Edit(config string) (adaptix.ListenerData, []byte, error) {
	var (
		listenerData adaptix.ListenerData
		conf         ServerlessConfig
		customdData  []byte
		err          error
	)

	if err = json.Unmarshal([]byte(config), &conf); err != nil {
		return listenerData, customdData, err
	}

	if conf.PollInterval > 0 {
		l.config.PollInterval = conf.PollInterval
	}
	if conf.TTLHours > 0 {
		l.config.TTLHours = conf.TTLHours
	}
	if conf.LambdaURL != "" {
		l.config.LambdaURL = conf.LambdaURL
	}

	l.transport.Config = l.config

	listenerData = adaptix.ListenerData{
		BindHost:  "lambda",
		BindPort:  l.config.Region,
		AgentAddr: l.config.LambdaURL,
	}
	if l.active {
		listenerData.Status = "Listen"
	} else {
		listenerData.Status = "Closed"
	}
	customdData, err = json.Marshal(l.config)

	return listenerData, customdData, err
}

func (l *Listener) Stop() error {
	l.active = false
	return l.transport.Stop()
}

func (l *Listener) GetProfile() ([]byte, error) {
	var buffer bytes.Buffer
	if err := json.NewEncoder(&buffer).Encode(l.config); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (l *Listener) InternalHandler(data []byte) (string, error) {
	var agentId string
	_ = data
	return agentId, nil
}

func patchProfileHost(uploadedFile string, lambdaURL string) string {
	decoded, err := base64.StdEncoding.DecodeString(uploadedFile)
	if err != nil {
		return uploadedFile
	}

	u, err := url.Parse(lambdaURL)
	if err != nil {
		return uploadedFile
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	replacement := host + ":" + port

	patched := strings.ReplaceAll(string(decoded), "LAMBDA_URL_HERE:443", replacement)
	patched = strings.ReplaceAll(patched, "LAMBDA_URL_HERE", host)

	return base64.StdEncoding.EncodeToString([]byte(patched))
}

func main() {}

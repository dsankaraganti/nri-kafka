package args

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/IBM/sarama"
	sdkArgs "github.com/newrelic/infra-integrations-sdk/v3/args"
	"github.com/newrelic/infra-integrations-sdk/v3/log"
)

// GlobalArgs represents the global arguments that were passed in
var GlobalArgs *ParsedArguments

// Define the default ports for zookeeper and JMX
const (
	defaultZookeeperPort = 2181
	defaultJMXPort       = 9999

	jmxHostDefaultAlias = "default"
)

// ParsedArguments is an special version of the config arguments that has advanced parsing
// to allow arguments to be consumed easier.
type ParsedArguments struct {
	sdkArgs.DefaultArgumentList

	ClusterName  string
	KafkaVersion sarama.KafkaVersion

	AutodiscoverStrategy string

	// Zookeeper autodiscovery. Only required if using zookeeper to autodiscover brokers
	ZookeeperHosts      []*ZookeeperHost
	ZookeeperAuthScheme string
	ZookeeperAuthSecret string
	ZookeeperPath       string
	PreferredListener   string

	// Bootstrap discovery. Only required if AutodiscoverStrategy is `bootstrap`
	BootstrapBroker *BrokerHost

	// Broker TLS options
	TLSCaFile             string
	TLSCertFile           string
	TLSKeyFile            string
	TLSInsecureSkipVerify bool

	// Producer and consumer connection info. No autodiscovery is supported for producers and consumers
	Producers []*JMXHost
	Consumers []*JMXHost

	// JMX defaults
	DefaultJMXPort     int
	DefaultJMXHost     string
	DefaultJMXUser     string
	DefaultJMXPassword string

	// JMX SSL options
	KeyStore           string
	KeyStorePassword   string
	TrustStore         string
	TrustStorePassword string

	MaxJMXConnections int
	NrJmx             string

	SaslMechanism string
	SaslUsername  string
	SaslPassword  string

	// Kerberos auth args
	SaslGssapiRealm                  string
	SaslGssapiServiceName            string
	SaslGssapiUsername               string
	SaslGssapiKeyTabPath             string
	SaslGssapiKerberosConfigPath     string
	SaslGssapiDisableFASTNegotiation bool

	// Collection configuration
	LocalOnlyCollection        bool
	ForceTopicSampleCollection bool
	CollectClusterMetrics      bool
	TopicMode                  string
	TopicList                  []string
	TopicRegex                 string
	TopicBucket                TopicBucket
	CollectTopicSize           bool
	CollectTopicOffset         bool
	EnableBrokerTopicMetricsV2 bool

	// Consumer offset arguments
	ConsumerOffset              bool
	ConsumerGroupRegex          *regexp.Regexp
	ConsumerGroupOffsetByTopic  bool
	InactiveConsumerGroupOffset bool

	Timeout int `default:"10000" help:"Timeout in milliseconds per single JMX query."`

	TopicSource string
}

// CollectBrokers returns whether we should collect broker metrics
func (args *ParsedArguments) CollectBrokers() bool {
	return len(args.ZookeeperHosts) > 0 || args.BootstrapBroker != nil
}

// TopicBucket is a struct that stores the information for bucketing topic collection
type TopicBucket struct {
	BucketNumber int
	NumBuckets   int
}

// ZookeeperHost is a storage struct for ZooKeeper connection information
type ZookeeperHost struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// BrokerHost is a storage struct for manual Broker connection information
type BrokerHost struct {
	Host          string
	KafkaPort     int    `json:"kafka_port"`
	KafkaProtocol string `json:"kafka_protocol"`
	JMXPort       int    `json:"jmx_port"`
	JMXUser       string `json:"jmx_user"`
	JMXPassword   string `json:"jmx_password"`
	SaslUsername  string `json:"sasl_username"`
	SaslPassword  string `json:"sasl_password"`
	SaslMechanism string `json:"sasl_mechanism"`
}

// JMXHost is a storage struct for producer and consumer connection information
type JMXHost struct {
	Name     string
	Host     string
	Port     int
	User     string
	Password string
}

// ParseArgs validates the arguments in argumentList and parses them
// into more easily used structs
func ParseArgs(a ArgumentList) (*ParsedArguments, error) {
	// Parse ZooKeeper hosts
	var zookeeperHosts []*ZookeeperHost
	err := json.Unmarshal([]byte(a.ZookeeperHosts), &zookeeperHosts)
	if err != nil {
		return nil, fmt.Errorf("failed to parse zookeepers from json: %s", err)
	}

	for _, zookeeperHost := range zookeeperHosts {
		// Set port to default if unset
		if zookeeperHost.Port == 0 {
			zookeeperHost.Port = defaultZookeeperPort
		}
	}

	if a.AutodiscoverStrategy != "zookeeper" && len(zookeeperHosts) != 0 {
		return nil, errors.New("Zookeeper hosts have been defined even though the autodiscovery strategy is not 'zookeeper'")
	}

	var brokerHost *BrokerHost
	if a.AutodiscoverStrategy == "bootstrap" {
		brokerHost = &BrokerHost{
			Host:          a.BootstrapBrokerHost,
			KafkaPort:     a.BootstrapBrokerKafkaPort,
			KafkaProtocol: a.BootstrapBrokerKafkaProtocol,
			JMXPort:       a.BootstrapBrokerJMXPort,
			JMXUser:       a.BootstrapBrokerJMXUser,
			JMXPassword:   a.BootstrapBrokerJMXPassword,
			SaslMechanism: a.SaslMechanism,
			SaslUsername:  a.SaslUsername,
			SaslPassword:  a.SaslPassword,
		}
		if brokerHost.JMXPort == 0 {
			brokerHost.JMXPort = defaultJMXPort
		}

		if brokerHost.JMXUser == "" {
			brokerHost.JMXUser = a.DefaultJMXUser
		}

		if brokerHost.JMXPassword == "" {
			brokerHost.JMXPassword = a.DefaultJMXPassword
		}
	}

	// Parse consumers
	consumers, err := unmarshalJMXHosts([]byte(a.Consumers), &a)
	if err != nil {
		log.Error("Failed to parse consumers from json")
		return nil, err
	}

	// Parse producers
	producers, err := unmarshalJMXHosts([]byte(a.Producers), &a)
	if err != nil {
		log.Error("Failed to parse producers from json")
		return nil, err
	}

	// Parse topics
	var topics []string
	if err = json.Unmarshal([]byte(a.TopicList), &topics); err != nil {
		log.Error("Failed to parse topics from json")
		return nil, err
	}

	// Parse topic bucket
	re := regexp.MustCompile(`(\d+)/(\d+)`)
	match := re.FindStringSubmatch(a.TopicBucket)
	if match == nil {
		log.Error("Failed to parse topic bucket. Must be of form `1/3`")
		return nil, errors.New("invalid topic bucket format")
	}

	bucketID, err := strconv.Atoi(match[1])
	if err != nil {
		log.Error("Bucket number %s is not parseable as an int", match[1])
		return nil, errors.New("invalid topic bucket")
	}
	numBuckets, err := strconv.Atoi(match[2])
	if err != nil {
		log.Error("Number of buckets %s is not parseable as an int", match[2])
		return nil, errors.New("invalid topic bucket")
	}

	if bucketID < 1 || bucketID > numBuckets {
		log.Error("Bucket number must be between 1 and the number of buckets. ('1/3' is okay, but '4/3' is not)")
		return nil, errors.New("invalid topic bucket")
	}

	topicBucket := TopicBucket{
		BucketNumber: bucketID,
		NumBuckets:   numBuckets,
	}

	var consumerGroupRegex *regexp.Regexp
	if a.ConsumerGroupRegex != "" {
		consumerGroupRegex, err = regexp.Compile(a.ConsumerGroupRegex)
		if err != nil {
			log.Error("Error parsing consumer_group_regex as a regex pattern")
			return nil, err
		}
	}

	version, err := sarama.ParseKafkaVersion(a.KafkaVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kafka version: %s", err)
	}

	log.Info("Processing new BrokerTopic metrics flag is : %v", a.EnableBrokerTopicMetricsV2)

	parsedArgs := &ParsedArguments{
		DefaultArgumentList:              a.DefaultArgumentList,
		AutodiscoverStrategy:             a.AutodiscoverStrategy,
		BootstrapBroker:                  brokerHost,
		TLSCaFile:                        a.TLSCaFile,
		TLSCertFile:                      a.TLSCertFile,
		TLSKeyFile:                       a.TLSKeyFile,
		TLSInsecureSkipVerify:            a.TLSInsecureSkipVerify,
		ClusterName:                      a.ClusterName,
		KafkaVersion:                     version,
		ZookeeperHosts:                   zookeeperHosts,
		ZookeeperAuthScheme:              a.ZookeeperAuthScheme,
		ZookeeperAuthSecret:              a.ZookeeperAuthSecret,
		ZookeeperPath:                    a.ZookeeperPath,
		PreferredListener:                a.PreferredListener,
		DefaultJMXUser:                   a.DefaultJMXUser,
		DefaultJMXPassword:               a.DefaultJMXPassword,
		MaxJMXConnections:                a.MaxJMXConnections,
		NrJmx:                            a.NrJmx,
		Producers:                        producers,
		Consumers:                        consumers,
		TopicMode:                        a.TopicMode,
		TopicList:                        topics,
		TopicRegex:                       a.TopicRegex,
		TopicBucket:                      topicBucket,
		Timeout:                          a.Timeout,
		KeyStore:                         a.KeyStore,
		KeyStorePassword:                 a.KeyStorePassword,
		TrustStore:                       a.TrustStore,
		TrustStorePassword:               a.TrustStorePassword,
		LocalOnlyCollection:              a.LocalOnlyCollection,
		ForceTopicSampleCollection:       a.ForceTopicSampleCollection,
		CollectTopicSize:                 a.CollectTopicSize,
		CollectTopicOffset:               a.CollectTopicOffset,
		ConsumerOffset:                   a.ConsumerOffset,
		ConsumerGroupRegex:               consumerGroupRegex,
		ConsumerGroupOffsetByTopic:       a.ConsumerGroupOffsetByTopic,
		InactiveConsumerGroupOffset:      a.InactiveConsumerGroupOffset,
		SaslMechanism:                    a.SaslMechanism,
		SaslUsername:                     a.SaslUsername,
		SaslPassword:                     a.SaslPassword,
		SaslGssapiRealm:                  a.SaslGssapiRealm,
		SaslGssapiServiceName:            a.SaslGssapiServiceName,
		SaslGssapiUsername:               a.SaslGssapiUsername,
		SaslGssapiKeyTabPath:             a.SaslGssapiKeyTabPath,
		SaslGssapiKerberosConfigPath:     a.SaslGssapiKerberosConfigPath,
		SaslGssapiDisableFASTNegotiation: a.SaslGssapiDisableFASTNegotiation,
		TopicSource:                      a.TopicSource,
		EnableBrokerTopicMetricsV2: 	  a.EnableBrokerTopicMetricsV2,
	}

	return parsedArgs, nil
}

// unmarshalJMXHosts parses the user-provided JSON map for a producer
// or consumers into a jmxHost structs and sets default values
func unmarshalJMXHosts(data []byte, a *ArgumentList) ([]*JMXHost, error) {
	// Parse the producer or consumer
	var v []*JMXHost
	if string(data) == jmxHostDefaultAlias {
		v = []*JMXHost{{}}
	} else if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}

	// Set default values
	for _, p := range v {
		if p.User == "" {
			p.User = a.DefaultJMXUser
		}
		if p.Password == "" {
			p.Password = a.DefaultJMXPassword
		}
		if p.Port == 0 {
			p.Port = a.DefaultJMXPort
		}
		if p.Host == "" {
			p.Host = a.DefaultJMXHost
		}
	}

	return v, nil
}

// ConsumerGroups is the structure to represent the whitelist for
// consumer_groups argument
type ConsumerGroups map[string]map[string][]int32

func unmarshalConsumerGroups(consumerOffset bool, consumerGroupsArg string) (ConsumerGroups, error) {
	// not in consumer offset mode so don't bother to unmarshal
	if !consumerOffset {
		return nil, nil
	}

	data := []byte(consumerGroupsArg)
	var consumerGroups ConsumerGroups
	if err := json.Unmarshal(data, &consumerGroups); err != nil {
		return nil, err
	}

	return consumerGroups, validateConsumerGroups(consumerGroups)
}

func validateConsumerGroups(groups ConsumerGroups) error {
	for groupName, topics := range groups {
		if len(topics) == 0 {
			return fmt.Errorf("consumer group '%s' contains no topics, at least one topic must be specified", groupName)
		}
	}

	return nil
}

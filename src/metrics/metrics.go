// Package metrics contains definitions for all JMX collected Metrics, and core collection
// methods for Brokers, Consumers, and Producers.
package metrics

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/newrelic/nri-kafka/src/connection"
	"github.com/newrelic/nrjmx/gojmx"

	"github.com/newrelic/infra-integrations-sdk/v3/data/attribute"
	"github.com/newrelic/infra-integrations-sdk/v3/data/metric"
	"github.com/newrelic/infra-integrations-sdk/v3/integration"
	"github.com/newrelic/infra-integrations-sdk/v3/jmx"
	"github.com/newrelic/infra-integrations-sdk/v3/log"
	"github.com/newrelic/nri-kafka/src/args"
)

// GetBrokerMetrics collects all Broker JMX metrics and stores them in sample
func GetBrokerMetrics(sample *metric.Set, conn connection.JMXConnection) {
	CollectMetricDefinitions(sample, GetFinalMetricSets(brokerMetricDefs, BrokerV2MetricDefs), nil, conn)
	CollectBrokerRequestMetrics(sample, brokerRequestMetricDefs, conn)
}

func GetFinalMetricSets(metricSets []*JMXMetricSet, v2MetricSets []*JMXMetricSet) []*JMXMetricSet {
	log.Info("Initial metric sets: %v , %v", metricSets, v2MetricSets)
    finalMetricSets := make([]*JMXMetricSet, 0, len(metricSets)+len(v2MetricSets))
    finalMetricSets = append(finalMetricSets, metricSets...)

    if args.GlobalArgs.EnableBrokerTopicMetricsV2 {
        finalMetricSets = append(finalMetricSets, v2MetricSets...)
    }

	log.Info("Final metric sets: %v", finalMetricSets)
    return finalMetricSets
}

// GetConsumerMetrics collects all Consumer metrics for the given
// consumerName and stores them in sample.
func GetConsumerMetrics(consumerName string, sample *metric.Set, conn connection.JMXConnection) {
	CollectMetricDefinitions(sample, consumerMetricDefs, applyConsumerName(consumerName), conn)
}

// GetProducerMetrics collects all Producer and Producer metrics for the given
// producerName and stores them in sample.
func GetProducerMetrics(producerName string, sample *metric.Set, conn connection.JMXConnection) {
	CollectMetricDefinitions(sample, producerMetricDefs, applyProducerName(producerName), conn)
}

// CollectTopicSubMetrics collects Topic metrics that are related to either a Producer or Consumer
//
// beanModifier is a function that is used to replace place holder with actual Consumer/Producer
// and Topic names for a given MBean
func CollectTopicSubMetrics(
	entity *integration.Entity,
	metricSets []*JMXMetricSet,
	beanModifier func(string, string) BeanModifier,
	conn connection.JMXConnection,
) {

	// need to title case the type so it matches the metric set of the parent entity
	titleEntityType := strings.Title(strings.TrimPrefix(entity.Metadata.Namespace, "ka-"))

	topicList, err := getTopicListFromJMX(entity.Metadata.Name, conn)
	if err != nil {
		log.Error("Failed to collect topic list for producer or consumer: %s", err)
		return
	}

	for _, topicName := range topicList {
		topicSample := entity.NewMetricSet("Kafka"+titleEntityType+"Sample",
			attribute.Attribute{Key: "displayName", Value: entity.Metadata.Name},
			attribute.Attribute{Key: "entityName", Value: fmt.Sprintf("%s:%s", strings.TrimPrefix(entity.Metadata.Namespace, "ka-"), entity.Metadata.Name)},
			attribute.Attribute{Key: "topic", Value: topicName},
		)

		CollectMetricDefinitions(topicSample, metricSets, beanModifier(entity.Metadata.Name, topicName), conn)
	}
}

// CollectBrokerRequestMetrics collects request metrics from brokers
func CollectBrokerRequestMetrics(sample *metric.Set, metricSets []*JMXMetricSet, conn connection.JMXConnection) {
	notFoundMetrics := make([]string, 0)

	for _, metricSet := range metricSets {
		beanName := metricSet.MBean

		// Return all the results under a specific mBean
		results, err := conn.QueryMBeanAttributes(beanName)
		// If we fail we don't want a total failure as other metrics can be collected even if a single failure/timout occurs
		if err != nil {
			if jmxErr, ok := gojmx.IsJMXError(err); ok {
				log.Error("Unable to execute JMX query for MBean '%s': %v", beanName, jmxErr)
				continue
			}
			log.Error("Connection error for %s:%s : %s", jmx.HostName(), jmx.Port(), err)
			os.Exit(1)
		}

		// For each metric to collect, populate the sample if it is
		// found in results, otherwise save the mBeanKey as not found
		for _, metricDef := range metricSet.MetricDefs {
			versionRollup := 0.0
			found := false
			// Newer versions of Kafka have nest the request metrics under a version, so we have to roll these up
			for _, attr := range results {
				if strings.HasPrefix(attr.Name, metricSet.MetricPrefix+metricDef.JMXAttr) && strings.HasSuffix(attr.Name, "attr=OneMinuteRate") {
					found = true
					rate, ok := attr.GetValue().(float64)
					if !ok {
						log.Warn("Got non-float64 value for a rate")
						continue
					}
					versionRollup += rate
				}
			}

			if !found {
				notFoundMetrics = append(notFoundMetrics, metricDef.Name)
				continue
			}

			if err := sample.SetMetric(metricDef.Name, versionRollup, metricDef.SourceType); err != nil {
				log.Error("Error setting value: %s", err)
			}
		}
	}

	if len(notFoundMetrics) > 0 {
		log.Warn("Can't find raw metrics in results for keys: %v", notFoundMetrics)
	}
}

// CollectMetricDefinitions collects the set of metrics from the current open JMX connection and add them to the sample
func CollectMetricDefinitions(sample *metric.Set, metricSets []*JMXMetricSet, beanModifier BeanModifier, conn connection.JMXConnection) {
	notFoundMetrics := make([]string, 0)

	for _, metricSet := range metricSets {
		beanName := metricSet.MBean

		if beanModifier != nil {
			beanName = beanModifier(beanName)
		}

		// Return all the results under a specific mBean
		results, err := conn.QueryMBeanAttributes(beanName)
		// If we fail we don't want a total failure as other metrics can be collected even if a single failure/timout occurs
		if err != nil {
			if jmxConnErr, ok := gojmx.IsJMXConnectionError(err); ok {
				log.Error("Connection error for %s:%s : %s", jmx.HostName(), jmx.Port(), jmxConnErr)
				os.Exit(1)
			}
			log.Error("Unable to execute JMX query for MBean '%s': %v", beanName, err)
			continue
		}

		attrsByName := make(map[string]*gojmx.AttributeResponse)
		for _, attr := range results {
			if attr.ResponseType == gojmx.ResponseTypeErr {
				log.Warn("Failed to process attribute for query: %s status: %s", attr.Name, attr.StatusMsg)
				continue
			}
			attrsByName[attr.Name] = attr
		}

		// For each metric to collect, populate the sample if it is
		// found in results, otherwise save the mBeanKey as not found
		for _, metricDef := range metricSet.MetricDefs {
			mBeanKey := metricSet.MetricPrefix + metricDef.JMXAttr
			if beanModifier != nil {
				mBeanKey = beanModifier(mBeanKey)
			}
			if value, ok := attrsByName[mBeanKey]; !ok {
				notFoundMetrics = append(notFoundMetrics, metricDef.Name)
			} else {
				if err := sample.SetMetric(metricDef.Name, value.GetValue(), metricDef.SourceType); err != nil {
					log.Error("Error setting value: %s", err)
				}
			}
		}
	}

	if len(notFoundMetrics) > 0 {
		log.Warn("Can't find raw metrics in results for keys: %v", notFoundMetrics)
	}
}

func getTopicListFromJMX(producer string, conn connection.JMXConnection) ([]string, error) {
	switch strings.ToLower(args.GlobalArgs.TopicMode) {
	case "none":
		return []string{}, nil
	case "list":
		return args.GlobalArgs.TopicList, nil
	case "regex":
		if args.GlobalArgs.TopicRegex == "" {
			return nil, errors.New("regex topic mode requires the topic_regex argument to be set")
		}

		pattern, err := regexp.Compile(args.GlobalArgs.TopicRegex)
		if err != nil {
			return nil, fmt.Errorf("failed to compile topic regex: %s", err)
		}

		allTopics, err := getAllTopicsFromJMX(producer, conn)
		if err != nil {
			return nil, fmt.Errorf("failed to get topics from client: %s", err)
		}

		filteredTopics := make([]string, 0, len(allTopics))
		for _, topic := range allTopics {
			if pattern.MatchString(topic) {
				filteredTopics = append(filteredTopics, topic)
			}
		}

		return filteredTopics, nil
	case "all":
		allTopics, err := getAllTopicsFromJMX(producer, conn)
		if err != nil {
			return nil, fmt.Errorf("failed to get topics from client: %s", err)
		}
		return allTopics, nil
	default:
		log.Error("Invalid topic mode %s", args.GlobalArgs.TopicMode)
		return nil, fmt.Errorf("invalid topic_mode '%s'", args.GlobalArgs.TopicMode)
	}

}

func getAllTopicsFromJMX(producer string, conn connection.JMXConnection) ([]string, error) {
	result, err := conn.QueryMBeanAttributes(fmt.Sprintf("kafka.producer:type=producer-topic-metrics,client-id=%s,topic=*", producer))
	// If we fail we don't want a total failure as other metrics can be collected even if a single failure/timout occurs
	if err != nil {
		if jmxErr, ok := gojmx.IsJMXError(err); ok {
			return nil, jmxErr
		}
		log.Error("Connection error for %s:%s : %s", jmx.HostName(), jmx.Port(), err)
		os.Exit(1)
	}

	r := regexp.MustCompile(`topic="?([^,"]+)"?`)
	uniqueTopics := make(map[string]struct{})
	for _, attr := range result {
		match := r.FindStringSubmatch(attr.Name)
		if match == nil {
			continue
		}

		uniqueTopics[match[1]] = struct{}{}
	}

	topics := make([]string, 0, len(uniqueTopics))
	for topic := range uniqueTopics {
		topics = append(topics, topic)
	}

	return topics, nil
}

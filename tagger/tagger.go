package tagger

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"regexp"
	"time"
	"unsafe"

	"github.com/BurntSushi/toml"
	"github.com/uber-go/zap"

	"github.com/lomik/graphite-clickhouse/config"
	"github.com/lomik/graphite-clickhouse/helper/clickhouse"
)

type Metric struct {
	Path []byte
	Tags map[string]bool
}

func unsafeString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

func countMetrics(body []byte) (int, error) {
	var namelen uint64
	bodyLen := len(body)
	var count, offset, readBytes int
	var err error

	for {
		if offset >= bodyLen {
			if offset == bodyLen {
				return count, nil
			}
			return 0, clickhouse.ErrClickHouseResponse
		}

		namelen, readBytes, err = clickhouse.ReadUvarint(body[offset:])
		if err != nil {
			return 0, err
		}
		offset += readBytes + int(namelen)
		count++
	}

	return 0, nil
}

func Make(rulesFilename string, date string, cfg *config.Config, logger zap.Logger) error {
	var start time.Time

	// Parse rules
	start = time.Now()
	rules := &Rules{}

	if _, err := toml.DecodeFile(rulesFilename, rules); err != nil {
		return err
	}

	var err error

	for i := 0; i < len(rules.Tag); i++ {
		tag := &rules.Tag[i]

		// compile and check regexp
		tag.re, err = regexp.Compile(tag.Regexp)
		if err != nil {
			return err
		}
		if tag.Equal != "" {
			tag.BytesEqual = []byte(tag.Equal)
		}
		if tag.Contains != "" {
			tag.BytesContains = []byte(tag.Contains)
		}
		if tag.HasPrefix != "" {
			tag.BytesHasPrefix = []byte(tag.HasPrefix)
		}
		if tag.HasSuffix != "" {
			tag.BytesHasSuffix = []byte(tag.HasSuffix)
		}
	}

	logger.Info("parse rules", zap.Duration("time", time.Since(start)))

	// Mark prefix tree
	start = time.Now()
	prefixTree := &PrefixTree{}

	for i := 0; i < len(rules.Tag); i++ {
		tag := &rules.Tag[i]

		if tag.BytesHasPrefix != nil {
			prefixTree.Add(tag.BytesHasPrefix, tag)
		}
	}
	logger.Info("make prefix tree", zap.Duration("time", time.Since(start)))

	// Read clickhouse
	start = time.Now()
	body, err := ioutil.ReadFile("tree.bin")
	if err != nil {
		return err
	}

	count, err := countMetrics(body)
	if err != nil {
		return err
	}

	metricList := make([]Metric, count)
	metricMap := make(map[string]*Metric, 0)

	var namelen uint64
	bodyLen := len(body)
	var offset, readBytes int

	for index := 0; ; index++ {
		if offset >= bodyLen {
			if offset == bodyLen {
				break
			}
			return clickhouse.ErrClickHouseResponse
		}

		namelen, readBytes, err = clickhouse.ReadUvarint(body[offset:])
		if err != nil {
			return err
		}

		metricList[index].Path = body[offset+readBytes : offset+readBytes+int(namelen)]
		metricList[index].Tags = make(map[string]bool)

		metricMap[unsafeString(metricList[index].Path)] = &metricList[index]

		offset += readBytes + int(namelen)
	}
	logger.Info("read and parse metrics", zap.Duration("time", time.Since(start)))

	start = time.Now()
	for i := 0; i < count; i++ {
		m := &metricList[i]

		// if i%1000 == 0 {
		// 	fmt.Println("tree", i)
		// }

		x := prefixTree
		j := 0
		for {
			if j >= len(m.Path) {
				break
			}

			x = x.Next[m.Path[j]]
			if x == nil {
				break
			}

			if x.Rules != nil {
				for _, rule := range x.Rules {
					rule.MatchAndMark(m)
				}
			}

			j++
		}
	}
	logger.Info("prefix tree match", zap.Duration("time", time.Since(start)))

	// start stupid match
	start = time.Now()
	for i := 0; i < len(metricList); i++ {
		for j := 0; j < len(rules.Tag); j++ {
			if rules.Tag[j].BytesHasPrefix != nil {
				// already checked by tree
				continue
			}

			rules.Tag[j].MatchAndMark(&metricList[i])
		}
	}
	logger.Info("fullscan match", zap.Duration("time", time.Since(start)))

	// copy from parents to childs
	start = time.Now()
	for _, m := range metricList {
		p := m.Path

		if len(p) > 0 && p[len(p)-1] == '.' {
			p = p[:len(p)-1]
		}

		for {
			index := bytes.LastIndexByte(p, '.')
			if index < 0 {
				break
			}

			parent := metricMap[unsafeString(p[:index+1])]

			if parent != nil {
				for k := range parent.Tags {
					m.Tags[k] = true
				}
			}

			p = p[:index]
		}
	}
	logger.Info("copy tags from parents to childs", zap.Duration("time", time.Since(start)))

	// copy from childs to parents
	start = time.Now()
	for _, m := range metricList {
		p := m.Path

		if len(p) > 0 && p[len(p)-1] == '.' {
			p = p[:len(p)-1]
		}

		for {
			index := bytes.LastIndexByte(p, '.')
			if index < 0 {
				break
			}

			parent := metricMap[unsafeString(p[:index+1])]

			if parent != nil {
				for k := range m.Tags {
					parent.Tags[k] = true
				}
			}

			p = p[:index]
		}
	}
	logger.Info("copy tags from childs to parents", zap.Duration("time", time.Since(start)))

	// print result with tags
	for _, m := range metricList {
		if len(m.Tags) != 0 {
			fmt.Println(m)
		}
	}

	// fmt.Println(rules)

	return nil
}

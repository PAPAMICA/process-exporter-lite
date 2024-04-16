package collector

import (
	"log"

	common "github.com/ncabatoff/process-exporter"
	"github.com/ncabatoff/process-exporter/proc"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// Use
	numprocsDesc = prometheus.NewDesc(
		"namedprocess_namegroup_num_procs",
		"number of processes in this group",
		[]string{"groupname"},
		nil)

	// Use
	cpuSecsDesc = prometheus.NewDesc(
		"namedprocess_namegroup_cpu_seconds_total",
		"Cpu user usage in seconds",
		[]string{"groupname", "mode"},
		nil)

	// Use
	readBytesDesc = prometheus.NewDesc(
		"namedprocess_namegroup_read_bytes_total",
		"number of bytes read by this group",
		[]string{"groupname"},
		nil)

	// Use
	writeBytesDesc = prometheus.NewDesc(
		"namedprocess_namegroup_write_bytes_total",
		"number of bytes written by this group",
		[]string{"groupname"},
		nil)

	// Use
	membytesDesc = prometheus.NewDesc(
		"namedprocess_namegroup_memory_bytes",
		"number of bytes of memory in use",
		[]string{"groupname", "memtype"},
		nil)

	// Technic
	scrapeErrorsDesc = prometheus.NewDesc(
		"namedprocess_scrape_errors",
		"general scrape errors: no proc metrics collected during a cycle",
		nil,
		nil)

	// Technic
	scrapeProcReadErrorsDesc = prometheus.NewDesc(
		"namedprocess_scrape_procread_errors",
		"incremented each time a proc's metrics collection fails",
		nil,
		nil)

	// Technic
	scrapePartialErrorsDesc = prometheus.NewDesc(
		"namedprocess_scrape_partial_errors",
		"incremented each time a tracked proc's metrics collection fails partially, e.g. unreadable I/O stats",
		nil,
		nil)

	// Use
	threadCpuSecsDesc = prometheus.NewDesc(
		"namedprocess_namegroup_thread_cpu_seconds_total",
		"Cpu user/system usage in seconds",
		[]string{"groupname", "threadname", "mode"},
		nil)
)

type (
	scrapeRequest struct {
		results chan<- prometheus.Metric
		done    chan struct{}
	}

	ProcessCollectorOption struct {
		ProcFSPath        string
		Children          bool
		Threads           bool
		GatherSMaps       bool
		Namer             common.MatchNamer
		Recheck           bool
		Debug             bool
		RemoveEmptyGroups bool
	}

	NamedProcessCollector struct {
		scrapeChan chan scrapeRequest
		*proc.Grouper
		threads              bool
		smaps                bool
		source               proc.Source
		scrapeErrors         int
		scrapeProcReadErrors int
		scrapePartialErrors  int
		debug                bool
	}
)

func NewProcessCollector(options ProcessCollectorOption) (*NamedProcessCollector, error) {
	fs, err := proc.NewFS(options.ProcFSPath, options.Debug)
	if err != nil {
		return nil, err
	}

	fs.GatherSMaps = options.GatherSMaps
	p := &NamedProcessCollector{
		scrapeChan: make(chan scrapeRequest),
		Grouper:    proc.NewGrouper(options.Namer, options.Children, options.Threads, options.Recheck, options.Debug, options.RemoveEmptyGroups),
		source:     fs,
		threads:    options.Threads,
		smaps:      options.GatherSMaps,
		debug:      options.Debug,
	}

	colErrs, _, err := p.Update(p.source.AllProcs())
	if err != nil {
		if options.Debug {
			log.Print(err)
		}
		return nil, err
	}
	p.scrapePartialErrors += colErrs.Partial
	p.scrapeProcReadErrors += colErrs.Read

	go p.start()

	return p, nil
}

// Describe implements prometheus.Collector.
func (p *NamedProcessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- cpuSecsDesc
	ch <- numprocsDesc
	ch <- readBytesDesc
	ch <- writeBytesDesc
	ch <- membytesDesc
	ch <- scrapeErrorsDesc
	ch <- scrapeProcReadErrorsDesc
	ch <- scrapePartialErrorsDesc
	ch <- threadCpuSecsDesc
}

// Collect implements prometheus.Collector.
func (p *NamedProcessCollector) Collect(ch chan<- prometheus.Metric) {
	req := scrapeRequest{results: ch, done: make(chan struct{})}
	p.scrapeChan <- req
	<-req.done
}

func (p *NamedProcessCollector) start() {
	for req := range p.scrapeChan {
		ch := req.results
		p.scrape(ch)
		req.done <- struct{}{}
	}
}

func (p *NamedProcessCollector) scrape(ch chan<- prometheus.Metric) {
	permErrs, groups, err := p.Update(p.source.AllProcs())
	p.scrapePartialErrors += permErrs.Partial
	if err != nil {
		p.scrapeErrors++
		log.Printf("error reading procs: %v", err)
	} else {
		for gname, gcounts := range groups {
			ch <- prometheus.MustNewConstMetric(numprocsDesc,
				prometheus.GaugeValue, float64(gcounts.Procs), gname)
			ch <- prometheus.MustNewConstMetric(membytesDesc,
				prometheus.GaugeValue, float64(gcounts.Memory.ResidentBytes), gname, "resident")
			ch <- prometheus.MustNewConstMetric(membytesDesc,
				prometheus.GaugeValue, float64(gcounts.Memory.VirtualBytes), gname, "virtual")
			ch <- prometheus.MustNewConstMetric(membytesDesc,
				prometheus.GaugeValue, float64(gcounts.Memory.VmSwapBytes), gname, "swapped")
			ch <- prometheus.MustNewConstMetric(cpuSecsDesc,
				prometheus.CounterValue, gcounts.CPUUserTime, gname, "user")
			ch <- prometheus.MustNewConstMetric(cpuSecsDesc,
				prometheus.CounterValue, gcounts.CPUSystemTime, gname, "system")
			ch <- prometheus.MustNewConstMetric(readBytesDesc,
				prometheus.CounterValue, float64(gcounts.ReadBytes), gname)
			ch <- prometheus.MustNewConstMetric(writeBytesDesc,
				prometheus.CounterValue, float64(gcounts.WriteBytes), gname)

			if p.smaps {
				ch <- prometheus.MustNewConstMetric(membytesDesc,
					prometheus.GaugeValue, float64(gcounts.Memory.ProportionalBytes), gname, "proportionalResident")
				ch <- prometheus.MustNewConstMetric(membytesDesc,
					prometheus.GaugeValue, float64(gcounts.Memory.ProportionalSwapBytes), gname, "proportionalSwapped")
			}

			if p.threads {
				for _, thr := range gcounts.Threads {
					ch <- prometheus.MustNewConstMetric(threadCpuSecsDesc,
						prometheus.CounterValue, float64(thr.CPUUserTime),
						gname, thr.Name, "user")
					ch <- prometheus.MustNewConstMetric(threadCpuSecsDesc,
						prometheus.CounterValue, float64(thr.CPUSystemTime),
						gname, thr.Name, "system")
				}
			}
		}
	}
	ch <- prometheus.MustNewConstMetric(scrapeErrorsDesc,
		prometheus.CounterValue, float64(p.scrapeErrors))
	ch <- prometheus.MustNewConstMetric(scrapeProcReadErrorsDesc,
		prometheus.CounterValue, float64(p.scrapeProcReadErrors))
	ch <- prometheus.MustNewConstMetric(scrapePartialErrorsDesc,
		prometheus.CounterValue, float64(p.scrapePartialErrors))
}

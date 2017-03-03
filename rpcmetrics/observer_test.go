package rpcmetrics

import (
	"fmt"
	"testing"
	"time"

	opentracing "github.com/opentracing/opentracing-go"
	"github.com/stretchr/testify/assert"
	"github.com/uber/jaeger-lib/metrics"
	u "github.com/uber/jaeger-lib/metrics/testutils"

	"github.com/opentracing/opentracing-go/ext"
	jaeger "github.com/uber/jaeger-client-go"
)

type testTracer struct {
	metrics *metrics.LocalFactory
	tracer  opentracing.Tracer
}

func withTestTracer(runTest func(tt *testTracer)) {
	sampler := jaeger.NewConstSampler(true)
	reporter := jaeger.NewInMemoryReporter()
	metrics := metrics.NewLocalFactory(time.Minute)
	observer := NewObserver(metrics, DefaultNameNormalizer)
	tracer, closer := jaeger.NewTracer(
		"test",
		sampler,
		reporter,
		jaeger.TracerOptions.Observer(observer))
	defer closer.Close()
	runTest(&testTracer{
		metrics: metrics,
		tracer:  tracer,
	})
}

func TestNonRPCSpan(t *testing.T) {
	withTestTracer(func(testTracer *testTracer) {
		span := testTracer.tracer.StartSpan("test")
		span.Finish()

		c, _ := testTracer.metrics.Snapshot()
		assert.Len(t, c, 0)
	})
}

func TestRPCSpans(t *testing.T) {
	withTestTracer(func(testTracer *testTracer) {
		ts := time.Now()
		finishOptions := opentracing.FinishOptions{
			FinishTime: ts.Add(50 * time.Millisecond),
		}

		testCases := []struct {
			name           string
			tag            opentracing.Tag
			opNameOverride string
		}{
			{name: "local-span", tag: opentracing.Tag{Key: "x", Value: "y"}},
			{name: "get-user", tag: ext.SpanKindRPCServer},
			{name: "get-user", tag: ext.SpanKindRPCServer, opNameOverride: "get-user-override"},
			{name: "get-user-client", tag: ext.SpanKindRPCClient},
		}

		for _, testCase := range testCases {
			span := testTracer.tracer.StartSpan(
				testCase.name,
				testCase.tag,
				opentracing.StartTime(ts),
			)
			if testCase.opNameOverride != "" {
				span.SetOperationName(testCase.opNameOverride)
			}
			span.FinishWithOptions(finishOptions)
		}

		u.AssertCounterMetrics(t,
			testTracer.metrics,
			u.ExpectedMetric{Name: "requests", Tags: endpointTags("local-span"), Value: 0},
			u.ExpectedMetric{Name: "success", Tags: endpointTags("local-span"), Value: 0},
			u.ExpectedMetric{Name: "requests", Tags: endpointTags("get-user"), Value: 1},
			u.ExpectedMetric{Name: "success", Tags: endpointTags("get-user"), Value: 1},
			u.ExpectedMetric{Name: "requests", Tags: endpointTags("get-user-override"), Value: 1},
			u.ExpectedMetric{Name: "success", Tags: endpointTags("get-user-override"), Value: 1},
			u.ExpectedMetric{Name: "requests", Tags: endpointTags("get-user-client"), Value: 0},
			u.ExpectedMetric{Name: "success", Tags: endpointTags("get-user-client"), Value: 0},
		)
		// TODO something wrong with string generation, .P99 should not be appended to the tag
		// as a result we cannot use u.AssertGaugeMetrics
		_, g := testTracer.metrics.Snapshot()
		assert.EqualValues(t, 51, g["request_latency_ms|endpoint=get-user.P99"])
	})
}

func TestTags(t *testing.T) {
	type tagTestCase struct {
		key     string
		value   interface{}
		metrics []u.ExpectedMetric
	}

	testCases := []tagTestCase{
		{key: "something", value: 42, metrics: []u.ExpectedMetric{
			{Name: "success", Value: 1},
			{Name: "requests", Value: 1},
		}},
		{key: "error", value: true, metrics: []u.ExpectedMetric{
			{Name: "failures", Value: 1},
			{Name: "requests", Value: 1},
		}},
		{key: "error", value: "true", metrics: []u.ExpectedMetric{
			{Name: "failures", Value: 1},
			{Name: "requests", Value: 1},
		}},
	}

	for i := 2; i <= 5; i++ {
		values := []interface{}{
			i * 100,
			uint16(i * 100),
			fmt.Sprintf("%d00", i),
		}
		for _, v := range values {
			testCases = append(testCases, tagTestCase{
				key: "http.status_code", value: v, metrics: []u.ExpectedMetric{
					{Name: "requests", Value: 1},
					{Name: fmt.Sprintf("http_status_code_%dxx", i), Value: 1},
				},
			})
		}
	}

	for _, tc := range testCases {
		testCase := tc // capture loop var
		for i := range testCase.metrics {
			testCase.metrics[i].Tags = endpointTags("span")
		}
		t.Run(fmt.Sprintf("%s-%v", testCase.key, testCase.value), func(t *testing.T) {
			withTestTracer(func(testTracer *testTracer) {
				span := testTracer.tracer.StartSpan("span", ext.SpanKindRPCServer)
				span.SetTag(testCase.key, testCase.value)
				span.Finish()
				u.AssertCounterMetrics(t, testTracer.metrics, testCase.metrics...)
			})
		})
	}
}
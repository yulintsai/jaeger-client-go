// Copyright (c) 2017 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package jaeger

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yulintsai/jaeger-client-go/testutils"
	"github.com/yulintsai/jaeger-client-go/thrift"
	j "github.com/yulintsai/jaeger-client-go/thrift-gen/jaeger"
)

var (
	testTracer, _ = NewTracer("svcName", NewConstSampler(false), NewNullReporter())
	jaegerTracer  = testTracer.(*Tracer)
)

func getThriftSpanByteLength(t *testing.T, span *Span) int {
	jSpan := BuildJaegerThrift(span)
	transport := thrift.NewTMemoryBufferLen(1000)
	protocolFactory := thrift.NewTCompactProtocolFactory()
	err := jSpan.Write(protocolFactory.GetProtocol(transport))
	require.NoError(t, err)
	return transport.Len()
}

func getThriftProcessByteLengthFromTracer(t *testing.T, tracer *Tracer) int {
	process := buildJaegerProcessThrift(tracer)
	return getThriftProcessByteLength(t, process)
}

func getThriftProcessByteLength(t *testing.T, process *j.Process) int {
	transport := thrift.NewTMemoryBufferLen(1000)
	protocolFactory := thrift.NewTCompactProtocolFactory()
	err := process.Write(protocolFactory.GetProtocol(transport))
	require.NoError(t, err)
	return transport.Len()
}

func newSpan() *Span {
	span := &Span{operationName: "test-span", tracer: jaegerTracer}
	span.context.samplingState = &samplingState{}
	return span
}

func TestEmitBatchOverhead(t *testing.T) {
	transport := thrift.NewTMemoryBufferLen(1000)
	protocolFactory := thrift.NewTCompactProtocolFactory()
	client := j.NewAgentClientFactory(transport, protocolFactory)

	span := newSpan()
	spanSize := getThriftSpanByteLength(t, span)

	tests := []int{1, 2, 14, 15, 377, 500, 65000, 0xFFFF}
	for i, n := range tests {
		transport.Reset()
		batch := make([]*j.Span, n)
		processTags := make([]*j.Tag, n)
		for x := 0; x < n; x++ {
			batch[x] = BuildJaegerThrift(span)
			processTags[x] = &j.Tag{}
		}
		process := &j.Process{ServiceName: "svcName", Tags: processTags}
		client.SeqId = -2 // this causes the longest encoding of varint32 as 5 bytes
		err := client.EmitBatch(&j.Batch{Process: process, Spans: batch})
		processSize := getThriftProcessByteLength(t, process)
		require.NoError(t, err)
		overhead := transport.Len() - n*spanSize - processSize
		assert.True(t, overhead <= emitBatchOverhead,
			"test %d, n=%d, expected overhead %d <= %d", i, n, overhead, emitBatchOverhead)
		t.Logf("span count: %d, overhead: %d", n, overhead)
	}
}

func TestUDPSenderFlush(t *testing.T) {
	agent, err := testutils.StartMockAgent()
	require.NoError(t, err)
	defer agent.Close()

	span := newSpan()
	spanSize := getThriftSpanByteLength(t, span)
	processSize := getThriftProcessByteLengthFromTracer(t, jaegerTracer)

	sender, err := NewUDPTransport(agent.SpanServerAddr(), 5*spanSize+processSize+emitBatchOverhead)
	require.NoError(t, err)
	udpSender := sender.(*udpSender)

	// test empty flush
	n, err := sender.Flush()
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// test early flush
	n, err = sender.Append(span)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "span should be in buffer, not flushed")
	buffer := udpSender.spanBuffer
	require.Equal(t, 1, len(buffer), "span should be in buffer, not flushed")
	assert.Equal(t, BuildJaegerThrift(span), buffer[0], "span should be in buffer, not flushed")

	n, err = sender.Flush()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, len(udpSender.spanBuffer), "buffer should become empty")
	assert.Equal(t, processSize, udpSender.byteBufferSize, "buffer size counter should be equal to the processSize")
	assert.Nil(t, buffer[0], "buffer should not keep reference to the span")

	for i := 0; i < 10000; i++ {
		batches := agent.GetJaegerBatches()
		if len(batches) > 0 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	batches := agent.GetJaegerBatches()
	require.Equal(t, 1, len(batches), "agent should have received the batch")
	require.Equal(t, 1, len(batches[0].Spans))
	assert.Equal(t, span.operationName, batches[0].Spans[0].OperationName)
}

func TestUDPSenderAppend(t *testing.T) {
	agent, err := testutils.StartMockAgent()
	require.NoError(t, err)
	defer agent.Close()

	span := newSpan()
	spanSize := getThriftSpanByteLength(t, span)
	processSize := getThriftProcessByteLengthFromTracer(t, jaegerTracer)

	tests := []struct {
		bufferSizeOffset      int
		expectFlush           bool
		expectSpansFlushed    int
		expectBatchesFlushed  int
		manualFlush           bool
		expectSpansFlushed2   int
		expectBatchesFlushed2 int
		description           string
	}{
		{1, false, 0, 0, true, 5, 1, "in test: buffer bigger than 5 spans"},
		{0, true, 5, 1, false, 0, 0, "in test: buffer fits exactly 5 spans"},
		{-1, true, 4, 1, true, 1, 1, "in test: buffer smaller than 5 spans"},
	}

	for _, test := range tests {
		bufferSize := 5*spanSize + test.bufferSizeOffset + processSize + emitBatchOverhead
		sender, err := NewUDPTransport(agent.SpanServerAddr(), bufferSize)
		require.NoError(t, err, test.description)

		agent.ResetJaegerBatches()
		for i := 0; i < 5; i++ {
			n, err := sender.Append(span)
			require.NoError(t, err, test.description)
			if i < 4 {
				assert.Equal(t, 0, n, test.description)
			} else {
				assert.Equal(t, test.expectSpansFlushed, n, test.description)
			}
		}
		if test.expectFlush {
			time.Sleep(5 * time.Millisecond)
		}
		batches := agent.GetJaegerBatches()
		require.Equal(t, test.expectBatchesFlushed, len(batches), test.description)
		var spans []*j.Span
		if test.expectBatchesFlushed > 0 {
			spans = batches[0].Spans
		}
		require.Equal(t, test.expectSpansFlushed, len(spans), test.description)
		for i := 0; i < test.expectSpansFlushed; i++ {
			assert.Equal(t, span.operationName, spans[i].OperationName, test.description)
		}

		if test.manualFlush {
			agent.ResetJaegerBatches()
			n, err := sender.Flush()
			require.NoError(t, err, test.description)
			assert.Equal(t, test.expectSpansFlushed2, n, test.description)

			time.Sleep(5 * time.Millisecond)
			batches = agent.GetJaegerBatches()
			require.Equal(t, test.expectBatchesFlushed2, len(batches), test.description)
			spans = []*j.Span{}
			if test.expectBatchesFlushed2 > 0 {
				spans = batches[0].Spans
			}
			require.Equal(t, test.expectSpansFlushed2, len(spans), test.description)
			for i := 0; i < test.expectSpansFlushed2; i++ {
				assert.Equal(t, span.operationName, spans[i].OperationName, test.description)
			}
		}

	}
}

func TestUDPSenderHugeSpan(t *testing.T) {
	agent, err := testutils.StartMockAgent()
	require.NoError(t, err)
	defer agent.Close()

	span := newSpan()
	spanSize := getThriftSpanByteLength(t, span)

	sender, err := NewUDPTransport(agent.SpanServerAddr(), spanSize/2+emitBatchOverhead)
	require.NoError(t, err)

	n, err := sender.Append(span)
	assert.Equal(t, errSpanTooLarge, err)
	assert.Equal(t, 1, n)
}

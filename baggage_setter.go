// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package jaeger

import (
	"github.com/opentracing/opentracing-go/log"
)

// baggageSetter is an actor that can set a baggage value on a Span given certain
// restrictions (eg. maxValueLength).
type baggageSetter interface {
	setBaggage(span *Span, key, value string)
}

// defaultBaggageSetter sets the baggage key:value on the span while respecting the
// maxValueLength and truncating the value if too long.
type defaultBaggageSetter struct {
	maxValueLength int
	metrics        *Metrics
}

func newDefaultBaggageSetter(maxValueLength int, metrics *Metrics) *defaultBaggageSetter {
	return &defaultBaggageSetter{
		maxValueLength: maxValueLength,
		metrics:        metrics,
	}
}

func (s *defaultBaggageSetter) setBaggage(span *Span, key, value string) {
	var truncated bool
	if len(value) > s.maxValueLength {
		truncated = true
		value = value[:s.maxValueLength]
		s.metrics.BaggageTruncate.Inc(1)
	}
	prevItem := span.context.baggage[key]
	logFields(span, key, value, prevItem, truncated, false)
	span.context = span.context.WithBaggageItem(key, value)
	s.metrics.BaggageUpdateSuccess.Inc(1)
}

// invalidBaggageSetter logs the invalid baggage key:value on the span.
type invalidBaggageSetter struct {
	metrics *Metrics
}

func newInvalidBaggageSetter(metrics *Metrics) *invalidBaggageSetter {
	return &invalidBaggageSetter{
		metrics: metrics,
	}
}

func (s *invalidBaggageSetter) setBaggage(span *Span, key, value string) {
	logFields(span, key, value, "", false, true)
	s.metrics.BaggageUpdateFailure.Inc(1)
}

func logFields(span *Span, key, value, prevItem string, truncated, invalid bool) {
	if !span.context.IsSampled() {
		return
	}
	fields := []log.Field{
		log.String("event", "baggage"),
		log.String("key", key),
		log.String("value", value),
	}
	if prevItem != "" {
		fields = append(fields, log.String("override", "true"))
	}
	if truncated {
		fields = append(fields, log.String("truncated", "true"))
	}
	if invalid {
		fields = append(fields, log.String("invalid", "true"))
	}
	span.logFieldsNoLocking(fields...)
}
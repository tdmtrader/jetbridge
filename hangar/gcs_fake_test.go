package hangar

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

type memoryObjectClient struct {
	mu                                                                                     sync.Mutex
	objects                                                                                map[string]memoryObject
	versions                                                                               map[string]map[int64]memoryObject
	nextGeneration                                                                         int64
	writeConditions                                                                        []storage.Conditions
	deleteConditions                                                                       []storage.Conditions
	readGenerations                                                                        []int64
	writeErr, closeErr, abortErr, attrsErr, deleteErr, newReaderErr, readErr, readCloseErr error
	abortCalls                                                                             int
	readErrAfter                                                                           int
	readBytes                                                                              int64
	blockRead, blockReadBody, blockWrite, retainVersions                                   bool
	afterAttrs, afterRead                                                                  func(string)
}

type memoryObject struct {
	data                             []byte
	metadata                         map[string]string
	generation, size, metageneration int64
	created                          time.Time
}

func newMemoryObjectClient() *memoryObjectClient {
	return &memoryObjectClient{objects: map[string]memoryObject{}, versions: map[string]map[int64]memoryObject{}, nextGeneration: 1}
}

func (client *memoryObjectClient) Object(bucket, key string) objectHandle {
	return &memoryObjectHandle{client: client, bucket: bucket, key: key}
}

func (client *memoryObjectClient) putRaw(key string, data []byte, metadata map[string]string, size int64) int64 {
	client.mu.Lock()
	defer client.mu.Unlock()
	generation := client.nextGeneration
	client.nextGeneration++
	client.objects[key] = memoryObject{data: append([]byte(nil), data...), metadata: cloneMetadata(metadata), generation: generation, size: size, metageneration: 1, created: time.Unix(generation, 0).UTC()}
	return generation
}

func (client *memoryObjectClient) replace(key string, data []byte, metadata map[string]string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if previous, ok := client.objects[key]; ok && client.retainVersions {
		if client.versions[key] == nil {
			client.versions[key] = map[int64]memoryObject{}
		}
		client.versions[key][previous.generation] = previous
	}
	generation := client.nextGeneration
	client.nextGeneration++
	client.objects[key] = memoryObject{data: append([]byte(nil), data...), metadata: cloneMetadata(metadata), generation: generation, size: int64(len(data)), metageneration: 1, created: time.Unix(generation, 0).UTC()}
}

func (client *memoryObjectClient) updateMetadata(key string, metadata map[string]string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	object := client.objects[key]
	object.metadata = cloneMetadata(metadata)
	object.metageneration++
	client.objects[key] = object
}

func (client *memoryObjectClient) lookupLocked(key string, generation int64) (memoryObject, bool) {
	current, found := client.objects[key]
	if generation == 0 {
		return current, found
	}
	if found && current.generation == generation {
		return current, true
	}
	version, found := client.versions[key][generation]
	return version, found
}

type memoryObjectHandle struct {
	client      *memoryObjectClient
	bucket, key string
	conditions  storage.Conditions
	generation  int64
}

func (handle *memoryObjectHandle) If(conditions storage.Conditions) objectHandle {
	copy := *handle
	copy.conditions = conditions
	return &copy
}
func (handle *memoryObjectHandle) Generation(generation int64) objectHandle {
	copy := *handle
	copy.generation = generation
	return &copy
}
func (handle *memoryObjectHandle) NewWriter(ctx context.Context) objectWriter {
	handle.client.mu.Lock()
	handle.client.writeConditions = append(handle.client.writeConditions, handle.conditions)
	handle.client.mu.Unlock()
	return &memoryObjectWriter{ctx: ctx, handle: handle}
}
func (handle *memoryObjectHandle) NewReader(ctx context.Context) (io.ReadCloser, error) {
	handle.client.mu.Lock()
	blocked, blockBody, closeErr := handle.client.blockRead, handle.client.blockReadBody, handle.client.readCloseErr
	newReaderErr, readErr, readErrAfter := handle.client.newReaderErr, handle.client.readErr, handle.client.readErrAfter
	object, found := handle.client.lookupLocked(handle.key, handle.generation)
	after := handle.client.afterRead
	handle.client.afterRead = nil
	if handle.generation != 0 {
		handle.client.readGenerations = append(handle.client.readGenerations, handle.generation)
	}
	handle.client.mu.Unlock()
	if after != nil {
		after(handle.key)
	}
	if blocked {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if newReaderErr != nil {
		return nil, newReaderErr
	}
	if !found {
		return nil, &googleapi.Error{Code: 404, Message: "missing generation"}
	}
	return &memoryObjectReader{ctx: ctx, reader: bytes.NewReader(object.data), block: blockBody, readErr: readErr, readErrAfter: readErrAfter, closeErr: closeErr, recordRead: handle.client.recordRead}, nil
}

func (client *memoryObjectClient) recordRead(count int) {
	client.mu.Lock()
	client.readBytes += int64(count)
	client.mu.Unlock()
}
func (handle *memoryObjectHandle) Attrs(context.Context) (objectAttrs, error) {
	handle.client.mu.Lock()
	if handle.client.attrsErr != nil {
		err := handle.client.attrsErr
		handle.client.mu.Unlock()
		return objectAttrs{}, err
	}
	object, found := handle.client.lookupLocked(handle.key, handle.generation)
	if !found {
		handle.client.mu.Unlock()
		return objectAttrs{}, &googleapi.Error{Code: 404, Message: "missing generation"}
	}
	attrs := objectAttrs{Generation: object.generation, Metageneration: object.metageneration, Size: object.size, Created: object.created, Metadata: cloneMetadata(object.metadata)}
	after := handle.client.afterAttrs
	handle.client.afterAttrs = nil
	handle.client.mu.Unlock()
	if after != nil {
		after(handle.key)
	}
	return attrs, nil
}
func (handle *memoryObjectHandle) Delete(context.Context) error {
	handle.client.mu.Lock()
	defer handle.client.mu.Unlock()
	handle.client.deleteConditions = append(handle.client.deleteConditions, handle.conditions)
	if handle.client.deleteErr != nil {
		return handle.client.deleteErr
	}
	object, found := handle.client.objects[handle.key]
	if !found {
		return &googleapi.Error{Code: 404, Message: "missing"}
	}
	if handle.conditions.GenerationMatch != 0 && handle.conditions.GenerationMatch != object.generation {
		return &googleapi.Error{Code: 412, Message: "generation mismatch"}
	}
	delete(handle.client.objects, handle.key)
	return nil
}

type memoryObjectWriter struct {
	ctx      context.Context
	handle   *memoryObjectHandle
	buffer   bytes.Buffer
	metadata map[string]string
	attrs    objectAttrs
	aborted  bool
}

func (writer *memoryObjectWriter) SetMetadata(metadata map[string]string) {
	writer.metadata = cloneMetadata(metadata)
}
func (writer *memoryObjectWriter) Write(content []byte) (int, error) {
	writer.handle.client.mu.Lock()
	blocked, err := writer.handle.client.blockWrite, writer.handle.client.writeErr
	writer.handle.client.mu.Unlock()
	if blocked {
		<-writer.ctx.Done()
		return 0, writer.ctx.Err()
	}
	if err != nil {
		return 0, err
	}
	return writer.buffer.Write(content)
}
func (writer *memoryObjectWriter) Close() error {
	client := writer.handle.client
	client.mu.Lock()
	defer client.mu.Unlock()
	if writer.aborted {
		return nil
	}
	if client.closeErr != nil {
		return client.closeErr
	}
	if writer.handle.conditions.DoesNotExist {
		if _, found := client.objects[writer.handle.key]; found {
			return &googleapi.Error{Code: 412, Message: "exists"}
		}
	}
	generation := client.nextGeneration
	client.nextGeneration++
	object := memoryObject{data: append([]byte(nil), writer.buffer.Bytes()...), metadata: cloneMetadata(writer.metadata), generation: generation, size: int64(writer.buffer.Len()), metageneration: 1, created: time.Unix(generation, 0).UTC()}
	client.objects[writer.handle.key] = object
	writer.attrs = objectAttrs{Generation: generation, Metageneration: 1, Size: object.size, Created: object.created, Metadata: cloneMetadata(object.metadata)}
	return nil
}
func (writer *memoryObjectWriter) Abort(error) error {
	writer.handle.client.mu.Lock()
	defer writer.handle.client.mu.Unlock()
	writer.aborted = true
	writer.handle.client.abortCalls++
	return writer.handle.client.abortErr
}
func (writer *memoryObjectWriter) Attrs() objectAttrs { return writer.attrs }

type memoryObjectReader struct {
	ctx          context.Context
	reader       io.Reader
	block        bool
	readErr      error
	readErrAfter int
	closeErr     error
	recordRead   func(int)
}

func (reader *memoryObjectReader) Read(buffer []byte) (int, error) {
	if reader.block {
		<-reader.ctx.Done()
		return 0, reader.ctx.Err()
	}
	if reader.readErr != nil {
		if reader.readErrAfter <= 0 {
			return 0, reader.readErr
		}
		if len(buffer) > reader.readErrAfter {
			buffer = buffer[:reader.readErrAfter]
		}
		count, err := reader.reader.Read(buffer)
		reader.readErrAfter -= count
		if reader.recordRead != nil {
			reader.recordRead(count)
		}
		return count, err
	}
	count, err := reader.reader.Read(buffer)
	if reader.recordRead != nil {
		reader.recordRead(count)
	}
	return count, err
}
func (reader *memoryObjectReader) Close() error { return reader.closeErr }

type blockingGCSReadCloser struct {
	started, closed      chan struct{}
	startOnce, closeOnce sync.Once
	mu                   sync.Mutex
	calls                int
}

func newBlockingGCSReadCloser() *blockingGCSReadCloser {
	return &blockingGCSReadCloser{started: make(chan struct{}), closed: make(chan struct{})}
}
func (reader *blockingGCSReadCloser) Read([]byte) (int, error) {
	reader.startOnce.Do(func() { close(reader.started) })
	<-reader.closed
	return 0, io.ErrClosedPipe
}
func (reader *blockingGCSReadCloser) Close() error {
	reader.mu.Lock()
	reader.calls++
	reader.mu.Unlock()
	reader.closeOnce.Do(func() { close(reader.closed) })
	return nil
}
func (reader *blockingGCSReadCloser) closeCalls() int {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.calls
}

type failingScratch struct {
	scratchFile
	err error
}

func (scratch *failingScratch) Write([]byte) (int, error) { return 0, scratch.err }

type expandingScratch struct {
	scratchFile
	extra int
}

type countingScratch struct {
	scratchFile
	written int64
}

func (scratch *countingScratch) Write(buffer []byte) (int, error) {
	count, err := scratch.scratchFile.Write(buffer)
	scratch.written += int64(count)
	return count, err
}

func (scratch *expandingScratch) Write(buffer []byte) (int, error) {
	count, err := scratch.scratchFile.Write(buffer)
	if err != nil {
		return count, err
	}
	if _, err := scratch.scratchFile.Write(make([]byte, scratch.extra)); err != nil {
		return count, err
	}
	return count, nil
}

func cloneMetadata(metadata map[string]string) map[string]string {
	copy := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copy[key] = value
	}
	return copy
}

type writeErrorObservingObjectClient struct {
	objectClient
	mu     sync.Mutex
	errors []error
}

func (client *writeErrorObservingObjectClient) Object(bucket, key string) objectHandle {
	return writeErrorObservingObjectHandle{objectHandle: client.objectClient.Object(bucket, key), client: client}
}

func (client *writeErrorObservingObjectClient) record(err error) {
	if err == nil {
		return
	}
	client.mu.Lock()
	client.errors = append(client.errors, err)
	client.mu.Unlock()
}

func (client *writeErrorObservingObjectClient) snapshot() []error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return append([]error(nil), client.errors...)
}

type writeErrorObservingObjectHandle struct {
	objectHandle
	client *writeErrorObservingObjectClient
}

func (handle writeErrorObservingObjectHandle) If(conditions storage.Conditions) objectHandle {
	return writeErrorObservingObjectHandle{objectHandle: handle.objectHandle.If(conditions), client: handle.client}
}

func (handle writeErrorObservingObjectHandle) Generation(generation int64) objectHandle {
	return writeErrorObservingObjectHandle{objectHandle: handle.objectHandle.Generation(generation), client: handle.client}
}

func (handle writeErrorObservingObjectHandle) NewWriter(ctx context.Context) objectWriter {
	return &writeErrorObservingObjectWriter{objectWriter: handle.objectHandle.NewWriter(ctx), client: handle.client}
}

type writeErrorObservingObjectWriter struct {
	objectWriter
	client *writeErrorObservingObjectClient
}

func (writer *writeErrorObservingObjectWriter) Write(buffer []byte) (int, error) {
	count, err := writer.objectWriter.Write(buffer)
	writer.client.record(err)
	return count, err
}

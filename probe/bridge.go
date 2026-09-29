package main

/*
#cgo CFLAGS: -I${SRCDIR}/../include/node
#cgo windows LDFLAGS: -L${SRCDIR}/../windows -lnode
#cgo linux LDFLAGS: -Wl,--unresolved-symbols=ignore-all
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup

#define NAPI_VERSION 8
#include <node_api.h>
#include <stdlib.h>

// napiGoExecuteCallback is the cgo //export trampoline of package napi. It is
// present in the final shared object's export table, so a raw Node-API call
// can be pointed at it directly — which is exactly how we build a JS function
// whose `data` argument is NULL (something the Go binding never does itself).
extern napi_value napiGoExecuteCallback(napi_env env, napi_callback_info info);

static napi_status probe_make_null_data_function(napi_env env,
                                                const char* name,
                                                size_t namelen,
                                                napi_value* out) {
  return napi_create_function(env, name, namelen,
                              napiGoExecuteCallback, NULL, out);
}

static void* probe_malloc(size_t n) { return malloc(n); }
static void  probe_free(void* p) { free(p); }

// --- raw-Node-API probes: settle questions the Go wrapper hides ------------

// Which of the two documented call patterns does the engine accept?
static napi_status probe_bigint_words_query(napi_env env, napi_value v, size_t* word_count) {
  return napi_get_value_bigint_words(env, v, NULL, word_count, NULL);
}
static napi_status probe_bigint_words_query_sign(napi_env env, napi_value v,
                                                 int* sign_bit, size_t* word_count) {
  return napi_get_value_bigint_words(env, v, sign_bit, word_count, NULL);
}
static napi_status probe_bigint_words_read(napi_env env, napi_value v,
                                           int* sign_bit, size_t* word_count,
                                           uint64_t* words) {
  return napi_get_value_bigint_words(env, v, sign_bit, word_count, words);
}

// napi_create_reference on a non-object value.
static napi_status probe_create_reference(napi_env env, napi_value v, uint32_t n, napi_ref* out) {
  return napi_create_reference(env, v, n, out);
}

// napi_call_function with a NULL recv.
static napi_status probe_call_null_recv(napi_env env, napi_value fn, napi_value* out) {
  return napi_call_function(env, NULL, fn, 0, NULL, out);
}

// napi_define_class with nothing attached: no properties, no finalizer, no
// callback data. Comparing the collectability of this class with the class
// produced by the binding's DefineClass tells apart "the binding's box leaks"
// from "the engine itself keeps classes built from a FunctionTemplate alive".
static napi_status probe_define_class_raw(napi_env env, const char* name,
                                          size_t namelen, napi_value* out) {
  return napi_define_class(env, name, namelen, napiGoExecuteCallback, NULL,
                           0, NULL, out);
}

// napi_set_instance_data, called twice; reports both statuses.
static napi_status probe_set_instance_data(napi_env env, void* data, napi_finalize fin, void* hint) {
  return napi_set_instance_data(env, data, fin, hint);
}
static void probe_noop_finalize(napi_env env, void* d, void* h) {}
static napi_status probe_set_instance_data_noop(napi_env env, void* data) {
  return napi_set_instance_data(env, data, probe_noop_finalize, NULL);
}

// --- raw threadsafe functions ---------------------------------------------
//
// The abort-with-a-thread-left sequence driven entirely through raw Node-API.
// The Go binding mirrors the engine's (state, thread_count) pair by hand, so
// the question "does the engine free the function here, or did the binding
// take a wrong step?" can only be answered by removing the binding from the
// picture.

static void probe_tsfn_noop_js(napi_env env, napi_value cb, void* ctx, void* data) {}

static napi_status probe_tsfn_create(napi_env env, napi_value fn, size_t initial,
                                     size_t max_queue, napi_threadsafe_function* out) {
  napi_value name;
  napi_status st = napi_create_string_utf8(env, "probe:raw-tsfn", NAPI_AUTO_LENGTH, &name);
  if (st != napi_ok) return st;
  return napi_create_threadsafe_function(env, fn, NULL, name, max_queue, initial,
                                         NULL, NULL, NULL, probe_tsfn_noop_js, out);
}

// probe_tsfn_acquire takes the mutex and nothing else, so on a function that
// has already been freed it faults at the lock — which is how "the engine has
// freed it" is observed without corrupting anything else.
static napi_status probe_tsfn_acquire(napi_threadsafe_function f) {
  return napi_acquire_threadsafe_function(f);
}
static napi_status probe_tsfn_release(napi_threadsafe_function f, int abort_mode) {
  return napi_release_threadsafe_function(
      f, abort_mode ? napi_tsfn_abort : napi_tsfn_release);
}
static napi_status probe_tsfn_call(napi_threadsafe_function f) {
  return napi_call_threadsafe_function(f, NULL, napi_tsfn_nonblocking);
}
*/
import "C"

import (
	"unsafe"

	"github.com/all-thoughts-are-broken/napi-go/napi"
)

// rawTsfn holds the handle across probe calls, so the driver can let the event
// loop turn in between.
var rawTsfn C.napi_threadsafe_function

// rawNullDataFunction creates a JS function via raw Node-API with a NULL
// data pointer. Calling it drives napiGoExecuteCallback into loadBox(nil).
func rawNullDataFunction(env napi.Env) (napi.Value, error) {
	var out C.napi_value
	st := C.probe_make_null_data_function(
		C.napi_env(env), C.CString("nullData"), 8, &out,
	)
	if st != C.napi_ok {
		return nil, &napi.StatusError{Code: napi.Status(st)}
	}
	return napi.Value(out), nil
}

func rawMalloc(n int) (unsafe.Pointer, error) {
	p := C.probe_malloc(C.size_t(n))
	if p == nil {
		return nil, &napi.StatusError{Code: napi.StatusGenericFailure, Message: "malloc failed"}
	}
	return p, nil
}

func rawFree(p unsafe.Pointer) { C.probe_free(p) }

// ---- raw API probes ---------------------------------------------------------

// rawBigintWords walks the three documented call patterns for
// napi_get_value_bigint_words and reports each status, so we can tell whether
// the engine or the binding is at fault when a query fails.
func rawBigintWords(env napi.Env, v napi.Value) (query, querySign, read string, words []uint64, sign int) {
	var wc C.size_t
	var csign C.int

	q := C.probe_bigint_words_query(C.napi_env(env), C.napi_value(v), &wc)
	qs := C.probe_bigint_words_query_sign(C.napi_env(env), C.napi_value(v), &csign, &wc)

	n := int(wc)
	var buf []uint64
	var ptr *C.uint64_t
	if n > 0 {
		buf = make([]uint64, n)
		ptr = (*C.uint64_t)(unsafe.Pointer(&buf[0]))
	}
	wc2 := wc
	rd := C.probe_bigint_words_read(C.napi_env(env), C.napi_value(v), &csign, &wc2, ptr)
	return napi.Status(q).String(), napi.Status(qs).String(), napi.Status(rd).String(),
		buf, int(csign)
}

func rawCreateReference(env napi.Env, v napi.Value, count uint32) string {
	var ref C.napi_ref
	st := C.probe_create_reference(C.napi_env(env), C.napi_value(v), C.uint32_t(count), &ref)
	if st == C.napi_ok && ref != nil {
		C.napi_delete_reference(C.napi_env(env), ref)
	}
	return napi.Status(st).String()
}

func rawCallNullRecv(env napi.Env, fn napi.Value) string {
	var out C.napi_value
	st := C.probe_call_null_recv(C.napi_env(env), C.napi_value(fn), &out)
	return napi.Status(st).String()
}

func rawSetInstanceData(env napi.Env, data unsafe.Pointer) string {
	return napi.Status(C.probe_set_instance_data_noop(C.napi_env(env), data)).String()
}

// rawDefineClass builds a class through raw Node-API: no properties, no
// callback data, no finalizer anywhere. Whatever the Go binding does with
// boxes is absent here, so its survival after a collection is the engine's
// own doing.
func rawDefineClass(env napi.Env) (napi.Value, error) {
	cname := C.CString("RawCls")
	defer C.free(unsafe.Pointer(cname))

	var out C.napi_value
	st := C.probe_define_class_raw(C.napi_env(env), cname, 6, &out)
	if st != C.napi_ok {
		return nil, &napi.StatusError{Code: napi.Status(st)}
	}
	return napi.Value(out), nil
}

// Raw TSFN steps. Phase numbers are addresses in the sequence, not cases:
//
//	0  create with two threads, unbounded queue
//	1  acquire          (takes the mutex; faults if the object is gone)
//	2  call             (Push)
//	3  release          (Release mode)
//	4  abort            (Abort mode)
//	5  create with one thread, unbounded queue
//	6  create with two threads, bounded queue
//	7  create with three threads, unbounded queue
//
// Each returns the engine's status, so the driver can interleave event-loop
// turns and see exactly which step first disagrees.
func rawTsfnStep(env napi.Env, fn napi.Value, phase int) string {
	create := func(initial, maxq int) string {
		var out C.napi_threadsafe_function
		st := C.probe_tsfn_create(C.napi_env(env), C.napi_value(fn),
			C.size_t(initial), C.size_t(maxq), &out)
		if st == C.napi_ok {
			rawTsfn = out
		}
		return napi.Status(st).String()
	}
	switch phase {
	case 0:
		return create(2, 0)
	case 5:
		return create(1, 0)
	case 6:
		return create(2, 64)
	case 7:
		return create(3, 0)
	case 1:
		return napi.Status(C.probe_tsfn_acquire(rawTsfn)).String()
	case 2:
		return napi.Status(C.probe_tsfn_call(rawTsfn)).String()
	case 3:
		return napi.Status(C.probe_tsfn_release(rawTsfn, 0)).String()
	case 4:
		return napi.Status(C.probe_tsfn_release(rawTsfn, 1)).String()
	}
	return "bad-phase"
}

package db_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Finite build event pages", func() {
	var build db.Build
	var table string
	BeforeEach(func() {
		var err error
		build, err = defaultTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
		table = fmt.Sprintf("team_build_events_%d", defaultTeam.ID())
	})
	insert := func(id int, payload string) {
		_, err := dbConn.Exec("INSERT INTO "+table+" (event_id,build_id,type,version,payload) VALUES ($1,$2,'log','5.0',$3)", id, build.ID(), payload)
		Expect(err).NotTo(HaveOccurred())
	}
	It("returns immediately when idle and keeps a revalidation cursor", func() {
		started := time.Now()
		page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(time.Since(started)).To(BeNumerically("<", 2*time.Second))
		Expect(page.CaughtUp).To(BeTrue())
		Expect(page.Finished).To(BeFalse())
		Expect(page.NextCursor).NotTo(BeNil())
	})
	It("splits oversized UTF-8 log events without losing metadata or duplicating text", func() {
		text := strings.Repeat("hé🙂", 20000)
		payload, _ := json.Marshal(map[string]any{"payload": text, "origin": map[string]any{"id": "task-1", "source": "stdout"}, "time": 12})
		insert(5, string(payload))
		cursor := ""
		combined := ""
		pages := 0
		for {
			page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: cursor, MaxBytes: 32768})
			Expect(err).NotTo(HaveOccurred())
			pages++
			Expect(pages).To(BeNumerically("<", 20))
			for _, e := range page.Events {
				if e.Event != "log" {
					continue
				}
				var data map[string]any
				Expect(json.Unmarshal(e.Data, &data)).To(Succeed())
				Expect(data["origin"]).To(Equal(map[string]any{"id": "task-1", "source": "stdout"}))
				combined += data["payload"].(string)
				Expect(e.ID).To(Equal("5"))
				Expect(e.Version).To(Equal("5.0"))
			}
			Expect(page.NextCursor).NotTo(BeNil())
			cursor = *page.NextCursor
			if page.CaughtUp {
				break
			}
			Expect(page.Truncated).To(BeTrue())
		}
		Expect(combined).To(Equal(text))
		Expect(pages).To(BeNumerically(">", 1))
	})
	It("preserves escaped NULs and makes progress under heavy JSON escaping", func() {
		text := strings.Repeat("<\x00🙂", 20000)
		payload, _ := json.Marshal(map[string]any{"payload": text, "origin": map[string]any{"id": "nul\x00origin"}})
		insert(5, string(payload))
		combined := ""
		cursor := ""
		for i := 0; i < 50; i++ {
			page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: cursor, MaxBytes: 65536})
			Expect(err).NotTo(HaveOccurred())
			Expect(page.NextCursor).NotTo(BeNil())
			Expect(*page.NextCursor).NotTo(Equal(cursor))
			cursor = *page.NextCursor
			for _, e := range page.Events {
				if e.Event == "log" {
					var data map[string]any
					Expect(json.Unmarshal(e.Data, &data)).To(Succeed())
					combined += data["payload"].(string)
				}
			}
			encoded, err := json.Marshal(page)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(encoded)).To(BeNumerically("<=", 256*1024))
			if page.CaughtUp {
				Expect(combined).To(Equal(text))
				return
			}
		}
		Fail("escaped log never completed")
	})

	It("detects a late lower-ID commit even after a terminal-looking snapshot", func() {
		dbConn.SetMaxOpenConns(4)
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		_, err = tx.Exec("INSERT INTO "+table+" (event_id,build_id,type,version,payload) VALUES (5,$1,'log','5.0',$2)", build.ID(), `{"payload":"late","origin":{"id":"task"}}`)
		Expect(err).NotTo(HaveOccurred())
		insert(8, `{"payload":"early","origin":{"id":"task"}}`)
		_, err = dbConn.Exec("UPDATE builds SET completed=true WHERE id=$1", build.ID())
		Expect(err).NotTo(HaveOccurred())
		page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.CaughtUp).To(BeTrue())
		Expect(page.Finished).To(BeTrue())
		Expect(page.NextCursor).NotTo(BeNil())
		Expect(tx.Commit()).To(Succeed())
		_, err = build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: *page.NextCursor})
		Expect(err).To(MatchError(db.ErrBuildEventStreamChanged))
	})
	It("rejects forged offset cursors instead of slicing the payload", func() {
		insert(1, `{"payload":"é","origin":{}}`)
		_, err := dbConn.Exec("INSERT INTO "+table+" (event_id,build_id,type,version,payload) VALUES (2,$1,'status','1.0','{\"status\":\"succeeded\"}')", build.ID())
		Expect(err).NotTo(HaveOccurred())
		forged := func(event, offset, prefix int) string {
			raw, _ := json.Marshal(map[string]any{"v": 1, "b": build.ID(), "e": event, "o": offset, "n": prefix})
			return base64.RawURLEncoding.EncodeToString(raw)
		}
		for _, cursor := range []string{forged(1, 1, 1), forged(1, 2, 1), forged(1, 7, 1), forged(2, 1, 2)} {
			_, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: cursor})
			Expect(err).To(MatchError(db.ErrBuildEventCursor), cursor)
		}
		_, err = dbConn.Exec("DELETE FROM " + table + " WHERE event_id = 1")
		Expect(err).NotTo(HaveOccurred())
		_, err = build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: forged(1, 1, 1)})
		Expect(err).To(MatchError(db.ErrBuildEventCursor))
	})
	It("allows legitimate event-ID gaps but rejects retained-output removal and wrong-build cursors", func() {
		insert(3, `{"payload":"first","origin":{}}`)
		insert(9, `{"payload":"second","origin":{}}`)
		page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.CaughtUp).To(BeTrue())
		other, err := defaultTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
		_, err = other.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: *page.NextCursor})
		Expect(err).To(MatchError(db.ErrBuildEventCursor))
		_, err = dbConn.Exec("UPDATE builds SET reap_time=now() WHERE id=$1", build.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: *page.NextCursor})
		Expect(err).To(MatchError(db.ErrBuildEventsReaped))
	})
	It("refuses canceled requests", func() {
		insert(7, `{"payload":"historic","origin":{"name":"old"}}`)
		page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Events).To(ContainElement(HaveField("ID", "7")))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = build.EventPage(ctx, atc.BuildEventPageRequest{})
		Expect(err).To(HaveOccurred())
	})
	It("fails explicitly on indivisible bounds", func() {
		_, err := dbConn.Exec("DELETE FROM " + table)
		Expect(err).NotTo(HaveOccurred())
		payload, _ := json.Marshal(map[string]any{"payload": strings.Repeat("x", 8*1024*1024)})
		insert(8, string(payload))
		_, err = build.EventPage(context.Background(), atc.BuildEventPageRequest{})
		Expect(err).To(MatchError(db.ErrBuildEventStoredTooLarge))
		_, err = dbConn.Exec("DELETE FROM " + table)
		Expect(err).NotTo(HaveOccurred())
		payload, _ = json.Marshal(map[string]any{"payload": "x", "origin": strings.Repeat("m", 65536)})
		insert(9, string(payload))
		_, err = build.EventPage(context.Background(), atc.BuildEventPageRequest{})
		Expect(err).To(MatchError(db.ErrBuildEventTooLarge))
	})
	It("retains terminal in-memory check output after the last-check pointer changes", func() {
		id := int64(3000000000)
		_, err := dbConn.Exec("UPDATE resources SET in_memory_build_id=$1,in_memory_build_status='failed' WHERE id=$2", id, defaultResource.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec("INSERT INTO check_build_events (event_id,build_id,type,version,payload) VALUES (4,$1,'log','5.0',$2)", id, `{"payload":"retained failure","origin":{}}`)
		Expect(err).NotTo(HaveOccurred())
		check, found, err := buildFactory.BuildForAPI(int(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		page, err := check.EventPage(context.Background(), atc.BuildEventPageRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Finished).To(BeTrue())
		Expect(page.Events).To(HaveLen(1))
	})
	It("measures full traversal at the stored-event bound and a large committed prefix", func() {
		// This is a coarse local cost measurement, not a latency assertion tied to hardware.
		text := strings.Repeat("a", 8*1024*1024-128)
		payload, _ := json.Marshal(map[string]any{"payload": text, "origin": map[string]any{"id": "task"}})
		insert(5, string(payload))
		start := time.Now()
		cursor := ""
		total, pages := 0, 0
		var slowest time.Duration
		for {
			tick := time.Now()
			page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: cursor, MaxBytes: 65536})
			Expect(err).NotTo(HaveOccurred())
			if elapsed := time.Since(tick); elapsed > slowest {
				slowest = elapsed
			}
			pages++
			Expect(pages).To(BeNumerically("<=", 130))
			for _, e := range page.Events {
				var d struct {
					Payload string `json:"payload"`
				}
				Expect(json.Unmarshal(e.Data, &d)).To(Succeed())
				total += len(d.Payload)
			}
			cursor = *page.NextCursor
			if page.CaughtUp {
				break
			}
		}
		Expect(total).To(Equal(len(text)))
		fmt.Fprintf(GinkgoWriter, "8MiB traversal: pages=%d total=%s slowest=%s\n", pages, time.Since(start), slowest)
		_, err := dbConn.Exec("DELETE FROM " + table)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec("INSERT INTO "+table+" (event_id,build_id,type,version,payload) SELECT n,$1,'log','5.0',$2 FROM generate_series(1,100000) n", build.ID(), `{"payload":"x"}`)
		Expect(err).NotTo(HaveOccurred())
		raw, _ := json.Marshal(map[string]any{"v": 1, "b": build.ID(), "e": 99999, "o": 0, "n": 99999})
		cursor = base64.RawURLEncoding.EncodeToString(raw)
		start = time.Now()
		page, err := build.EventPage(context.Background(), atc.BuildEventPageRequest{Cursor: cursor})
		Expect(err).NotTo(HaveOccurred())
		Expect(page.CaughtUp).To(BeTrue())
		fmt.Fprintf(GinkgoWriter, "100k prefix + team old/new predicate: %s\n", time.Since(start))
	})

})

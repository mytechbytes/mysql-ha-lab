package in.mytechbytes.mysqlha.labapp.health;

import in.mytechbytes.mysqlha.labapp.configs.Retry;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.atomic.AtomicLong;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;
import org.springframework.transaction.PlatformTransactionManager;
import org.springframework.transaction.support.TransactionTemplate;

/**
 * Every second: one tiny write through the writer pool and one read through the reader pool,
 * WITHOUT retry. Each up -> down / down -> up change is logged, counted and kept in a history.
 *
 *   WARN  writer DOWN since 13:02:11.204: ERROR 0 (08S01): Communications link failure
 *   WARN  writer UP again at 13:02:17.530 after 6326 ms
 */
@Component
public class DbProbe {

    private static final Logger log = LoggerFactory.getLogger("probe");
    private static final int HISTORY = 50;

    public record Outage(String target, Instant start, Instant end, long millis, String firstError) {}

    private final class State {
        final String target;
        volatile boolean up = true;
        volatile Instant downSince;
        volatile String firstError;
        final AtomicLong upGauge = new AtomicLong(1);
        final AtomicLong lastStartMs = new AtomicLong();
        final AtomicLong lastEndMs = new AtomicLong();
        final AtomicLong lastDurationMs = new AtomicLong();
        final Timer outageTimer;

        State(String target, MeterRegistry r) {
            this.target = target;
            Gauge.builder("app.db.probe.up", upGauge, AtomicLong::get)
                    .description("1 = path healthy, 0 = failing").tag("target", target).register(r);
            Gauge.builder("app.db.outage.last.start.seconds", lastStartMs, a -> a.get() / 1000.0)
                    .description("Unix time the last outage started").tag("target", target).register(r);
            Gauge.builder("app.db.outage.last.end.seconds", lastEndMs, a -> a.get() / 1000.0)
                    .description("Unix time the last outage ended").tag("target", target).register(r);
            Gauge.builder("app.db.outage.last.duration.seconds", lastDurationMs, a -> a.get() / 1000.0)
                    .description("Length of the last finished outage").tag("target", target).register(r);
            this.outageTimer = Timer.builder("app.db.outage")
                    .description("Finished outages and their length").tag("target", target).register(r);
        }
    }

    private final JdbcClient jdbc;
    private final TransactionTemplate probeWrite;
    private final TransactionTemplate probeRead;
    private final State writer;
    private final State reader;
    private final Deque<Outage> history = new ArrayDeque<>();

    public DbProbe(JdbcClient jdbc, PlatformTransactionManager tm, MeterRegistry registry) {
        this.jdbc = jdbc;
        this.probeWrite = new TransactionTemplate(tm);
        this.probeWrite.setTimeout(2);
        this.probeRead = new TransactionTemplate(tm);
        this.probeRead.setReadOnly(true);
        this.probeRead.setTimeout(2);
        this.writer = new State("writer", registry);
        this.reader = new State("reader", registry);
    }

    @Scheduled(fixedDelayString = "${app.probe.interval-ms:1000}")
    public void probeWriter() {
        check(writer, () -> probeWrite.execute(s -> jdbc.sql("""
                INSERT INTO heartbeat (id, beat_at, node) VALUES (1, NOW(3), @@hostname) AS n
                ON DUPLICATE KEY UPDATE beat_at = n.beat_at, node = n.node""").update()));
    }

    @Scheduled(fixedDelayString = "${app.probe.interval-ms:1000}")
    public void probeReader() {
        check(reader, () -> probeRead.execute(s -> jdbc.sql("SELECT 1").query(Integer.class).single()));
    }

    private void check(State st, Runnable call) {
        Instant started = Instant.now();
        try {
            call.run();
            if (!st.up) markUp(st, Instant.now());
        } catch (RuntimeException e) {
            if (st.up) markDown(st, started, Retry.describe(e));
        }
    }

    private synchronized void markDown(State st, Instant at, String error) {
        st.up = false;
        st.downSince = at;
        st.firstError = error;
        st.upGauge.set(0);
        st.lastStartMs.set(at.toEpochMilli());
        log.warn("{} DOWN since {}: {}", st.target, at, error);
    }

    private synchronized void markUp(State st, Instant at) {
        Duration d = Duration.between(st.downSince, at);
        st.up = true;
        st.upGauge.set(1);
        st.lastEndMs.set(at.toEpochMilli());
        st.lastDurationMs.set(d.toMillis());
        st.outageTimer.record(d);
        history.addFirst(new Outage(st.target, st.downSince, at, d.toMillis(), st.firstError));
        while (history.size() > HISTORY) history.removeLast();
        log.warn("{} UP again at {} after {} ms", st.target, at, d.toMillis());
    }

    public synchronized Map<String, Object> snapshot() {
        Map<String, Object> m = new LinkedHashMap<>();
        for (State st : List.of(writer, reader)) {
            Map<String, Object> s = new LinkedHashMap<>();
            s.put("up", st.up);
            if (!st.up) {
                s.put("downSince", st.downSince);
                s.put("downForMs", Duration.between(st.downSince, Instant.now()).toMillis());
                s.put("firstError", st.firstError);
            }
            m.put(st.target, s);
        }
        m.put("outages", new ArrayList<>(history));
        return m;
    }
}

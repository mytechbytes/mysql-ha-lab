package in.mytechbytes.mysqlha.labapp.orders;

import in.mytechbytes.mysqlha.labapp.db.Retry;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.context.event.EventListener;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;

import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

@Component
public class OrderLoadGenerator {

    private static final Logger log = LoggerFactory.getLogger("load");

    private final OrderService orders;
    private final Counter staleReads;
    private final AtomicBoolean running;

    private final AtomicLong seq = new AtomicLong();
    private final AtomicLong ok = new AtomicLong();
    private final AtomicLong retried = new AtomicLong();
    private final AtomicLong failed = new AtomicLong();
    private final AtomicLong dedup = new AtomicLong();
    private final AtomicLong stale = new AtomicLong();
    private final AtomicLong maxMs = new AtomicLong();

    public OrderLoadGenerator(OrderService orders, MeterRegistry registry,
                              @Value("${app.load.enabled:false}") boolean enabled) {
        this.orders = orders;
        this.running = new AtomicBoolean(enabled);
        // ops, outcomes, latency and errors are recorded by Retry (app_db_*); only stale reads are counted here
        this.staleReads = Counter.builder("app.db.stale.reads")
                .description("Reads on a secondary that did not see our own write yet").register(registry);
    }

    /** LOAD_ENABLED=true only takes effect after startup (tables exist). */
    private final AtomicBoolean ready = new AtomicBoolean(false);

    @EventListener(ApplicationReadyEvent.class)
    public void onReady() { ready.set(true); }

    public void start() { running.set(true); }
    public void stop()  { running.set(false); }

    public Map<String, Object> status() {
        return Map.of("running", running.get(), "iterations", seq.get(),
                "ok", ok.get(), "retried", retried.get(), "failed", failed.get(),
                "dedup", dedup.get(), "staleReads", stale.get(), "maxCreateMs", maxMs.get());
    }

    public void reset() {
        for (AtomicLong a : new AtomicLong[]{seq, ok, retried, failed, dedup, stale, maxMs}) a.set(0);
    }

    @Scheduled(fixedDelayString = "${app.load.delay-ms:200}")
    public void tick() {
        if (!ready.get() || !running.get()) return;
        long n = seq.incrementAndGet();

        // 1. CREATE on the primary (retried inside OrderService)
        long t0 = System.nanoTime();
        OrderService.WriteResult w;
        try {
            w = orders.create("load", 1, null);
        } catch (RuntimeException e) {
            failed.incrementAndGet();
            log.info("FAIL  #{} create {}", n, Retry.describe(e));
            return;
        }
        long ms = TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - t0);
        maxMs.accumulateAndGet(ms, Math::max);
        if (w.attempts() > 1) retried.incrementAndGet();
        else                  ok.incrementAndGet();
        if (w.deduplicated()) dedup.incrementAndGet();

        // 2. READ it back from a secondary: did replication keep up?
        String read;
        try {
            var r = orders.find(w.order().id(), false);
            if (r.value().isPresent()) {
                read = "read " + r.value().get().readFrom();
            } else {
                stale.incrementAndGet();
                staleReads.increment();
                String fresh = orders.find(w.order().id(), true).value().map(Order::readFrom).orElse("?");
                read = "read STALE -> fresh " + fresh;
            }
        } catch (RuntimeException e) {
            read = "read FAIL " + Retry.describe(e);
        }

        log.info("OK    #{} create id={} node={} attempts={} {}ms{} | {}",
                n, w.order().id(), w.order().writtenOn(), w.attempts(), ms,
                w.deduplicated() ? " (dedup)" : "", read);
    }

}

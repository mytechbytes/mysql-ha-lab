package in.mytechbytes.mysqlha.labapp.orders;

import in.mytechbytes.mysqlha.labapp.configs.Retry;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Component;

import java.util.Map;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

@Component
public class LoadGenerator {

    private static final Logger log = LoggerFactory.getLogger("load");

    private final OrderService orders;
    private final MeterRegistry registry;
    private final Timer createTimer;
    private final AtomicBoolean running;

    private final AtomicLong seq = new AtomicLong();
    private final AtomicLong ok = new AtomicLong();
    private final AtomicLong retried = new AtomicLong();
    private final AtomicLong failed = new AtomicLong();
    private final AtomicLong dedup = new AtomicLong();
    private final AtomicLong stale = new AtomicLong();
    private final AtomicLong maxMs = new AtomicLong();

    public LoadGenerator(OrderService orders, MeterRegistry registry,
                         @Value("${app.load.enabled:false}") boolean enabled) {
        this.orders = orders;
        this.registry = registry;
        this.running = new AtomicBoolean(enabled);
        this.createTimer = Timer.builder("app.create.duration")
                .description("CREATE including retries").publishPercentileHistogram().register(registry);
    }

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
        if (!running.get()) return;
        long n = seq.incrementAndGet();

        // 1. CREATE on the primary (retried inside OrderService)
        long t0 = System.nanoTime();
        OrderService.WriteResult w;
        try {
            w = orders.create("load", 1, null);
        } catch (RuntimeException e) {
            failed.incrementAndGet();
            count("create", "fail");
            log.info("FAIL  #{} create {}", n, Retry.describe(e));
            return;
        }
        long ms = TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - t0);
        createTimer.record(ms, TimeUnit.MILLISECONDS);
        maxMs.accumulateAndGet(ms, Math::max);
        if (w.attempts() > 1) { retried.incrementAndGet(); count("create", "retried"); }
        else                  { ok.incrementAndGet();      count("create", "ok"); }
        if (w.deduplicated()) dedup.incrementAndGet();

        // 2. READ it back from a secondary: did replication keep up?
        String read;
        try {
            var r = orders.find(w.order().id(), false);
            if (r.value().isPresent()) {
                read = "read " + r.value().get().readFrom();
                count("read", "ok");
            } else {
                stale.incrementAndGet();
                count("read", "stale");
                String fresh = orders.find(w.order().id(), true).value().map(Order::readFrom).orElse("?");
                read = "read STALE -> fresh " + fresh;
            }
        } catch (RuntimeException e) {
            count("read", "fail");
            read = "read FAIL " + Retry.describe(e);
        }

        log.info("OK    #{} create id={} node={} attempts={} {}ms{} | {}",
                n, w.order().id(), w.order().writtenOn(), w.attempts(), ms,
                w.deduplicated() ? " (dedup)" : "", read);
    }

    private void count(String op, String result) {
        registry.counter("app.ops", "op", op, "result", result).increment();
    }
}

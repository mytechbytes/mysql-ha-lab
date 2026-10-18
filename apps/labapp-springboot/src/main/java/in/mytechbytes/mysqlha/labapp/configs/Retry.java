package in.mytechbytes.mysqlha.labapp.configs;

import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import java.sql.SQLException;
import java.sql.SQLTransientConnectionException;
import java.sql.SQLTransientException;
import java.util.Set;
import java.util.function.Supplier;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.dao.TransientDataAccessException;
import org.springframework.jdbc.CannotGetJdbcConnectionException;
import org.springframework.stereotype.Component;

@Component
public class Retry {

    public enum Kind { READ, WRITE;
        String tag() { return name().toLowerCase(); } }

    public record Result<T>(T value, int attempts) {}

    private static final Logger log = LoggerFactory.getLogger(Retry.class);

    private static final Set<Integer> RETRYABLE = Set.of(
            1205, 1213, 1290, 1836, 2002, 2003, 2006, 2013, 3100, 3101);

    private final int maxAttempts;
    private final MeterRegistry registry;

    public Retry(@Value("${app.retry.max-attempts:8}") int maxAttempts, MeterRegistry registry) {
        this.maxAttempts = maxAttempts;
        this.registry = registry;
    }

    /** body = one COMPLETE transaction (TransactionTemplate.execute). */
    public <T> Result<T> run(Kind kind, String op, Supplier<T> body) {
        Timer.Sample sample = Timer.start(registry);
        for (int attempt = 1; ; attempt++) {
            try {
                T value = body.get();
                finish(sample, kind, attempt == 1 ? "ok" : "retried");
                return new Result<>(value, attempt);
            } catch (RuntimeException e) {
                registry.counter("app.db.attempt.errors", "kind", kind.tag(), "error", errorTag(e)).increment();
                if (attempt >= maxAttempts || !isRetryable(e)) {
                    finish(sample, kind, "failed");
                    log.error("{} FAILED after {} attempt(s): {}", op, attempt, describe(e));
                    throw e;
                }
                long sleep = Math.min(200L << (attempt - 1), 2_000L);
                log.warn("{} attempt {} failed: {} -> retry in {} ms", op, attempt, describe(e), sleep);
                try {
                    Thread.sleep(sleep);
                } catch (InterruptedException ie) {
                    Thread.currentThread().interrupt();
                    finish(sample, kind, "failed");
                    throw e;
                }
            }
        }
    }

    private void finish(Timer.Sample sample, Kind kind, String outcome) {
        registry.counter("app.db.ops", "kind", kind.tag(), "outcome", outcome).increment();
        sample.stop(Timer.builder("app.db.op")
                .description("DB operation incl. retries")
                .tag("kind", kind.tag())
                .publishPercentileHistogram()
                .register(registry));
    }

    public static boolean isRetryable(Throwable e) {
        for (Throwable t = e; t != null; t = t.getCause()) {
            if (t instanceof CannotGetJdbcConnectionException
                    || t instanceof TransientDataAccessException
                    || t instanceof SQLTransientException) return true;
            if (t instanceof SQLException s) {
                String st = s.getSQLState();
                if (RETRYABLE.contains(s.getErrorCode()) || (st != null && st.startsWith("08"))) return true;
            }
        }
        return false;
    }

    /** Short, low-cardinality label: "1290", "2013", "08S01", "pool-timeout", ... */
    public static String errorTag(Throwable e) {
        for (Throwable t = e; t != null; t = t.getCause()) {
            if (t instanceof SQLTransientConnectionException) return "pool-timeout";
            if (t instanceof SQLException s) {
                if (s.getErrorCode() != 0) return String.valueOf(s.getErrorCode());
                if (s.getSQLState() != null) return s.getSQLState();
            }
        }
        return e.getClass().getSimpleName();
    }

    public static String describe(Throwable e) {
        for (Throwable t = e; t != null; t = t.getCause()) {
            if (t instanceof SQLException s && (s.getErrorCode() != 0 || s.getSQLState() != null)) {
                String msg = String.valueOf(s.getMessage());
                return "ERROR " + s.getErrorCode() + " (" + s.getSQLState() + "): "
                        + (msg.length() > 120 ? msg.substring(0, 120) + "..." : msg);
            }
        }
        return e.getClass().getSimpleName() + ": " + e.getMessage();
    }
}

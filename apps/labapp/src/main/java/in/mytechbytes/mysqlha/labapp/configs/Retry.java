package in.mytechbytes.mysqlha.labapp.configs;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.dao.TransientDataAccessException;
import org.springframework.jdbc.CannotGetJdbcConnectionException;
import org.springframework.stereotype.Component;

import java.sql.SQLException;
import java.sql.SQLTransientException;
import java.util.Set;
import java.util.function.Supplier;

@Component
public class Retry {
    private static final Logger log = LoggerFactory.getLogger(Retry.class);

    /** MySQL error codes a failover/switchover produces. */
    private static final Set<Integer> RETRYABLE = Set.of(
            1205,        // lock wait timeout
            1213,        // deadlock
            1290, 1836,  // read-only: reached the old primary during a switchover
            2002, 2003,  // cannot connect
            2006, 2013,  // server gone away / lost connection
            3100, 3101); // Group Replication rolled the transaction back

    public record Result<T>(T value, int attempts) {}
    private final int maxAttempts;

    public Retry(@Value("${app.retry.max-attempts:8}") int maxAttempts) {
        this.maxAttempts = maxAttempts;
    }

    public <T> Result<T> run(String op, Supplier<T> body) {
        for (int attempt = 1; ; attempt++) {
            try {
                return new Result<>(body.get(), attempt);
            } catch (RuntimeException e) {
                if (attempt >= maxAttempts || !isRetryable(e)) {
                    throw e;
                }
                long sleep = Math.min(200L << (attempt - 1), 2_000L);
                log.warn("{} attempt {} failed: {} -> retry in {} ms", op, attempt, describe(e), sleep);
                try {
                    Thread.sleep(sleep);
                } catch (InterruptedException ie) {
                    Thread.currentThread().interrupt();
                    throw e;
                }
            }
        }
    }

    public static boolean isRetryable(Throwable e) {
        for (Throwable t = e; t != null; t = t.getCause()) {
            if (t instanceof CannotGetJdbcConnectionException
                    || t instanceof TransientDataAccessException
                    || t instanceof SQLTransientException) {
                return true;
            }
            if (t instanceof SQLException s) {
                String state = s.getSQLState();
                if (RETRYABLE.contains(s.getErrorCode()) || (state != null && state.startsWith("08"))) {
                    return true;
                }
            }
        }
        return false;
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

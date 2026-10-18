package in.mytechbytes.mysqlha.labapp.web;

import in.mytechbytes.mysqlha.labapp.db.Retry;
import java.util.Map;
import org.springframework.dao.DataAccessException;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;

/**
 * For every controller: a database error that survived all retries (or was not retryable)
 * becomes 503 with the MySQL error text, instead of a generic 500.
 *
 *   {"error":"ERROR 2013 (HY000): Lost connection to MySQL server during query"}
 */
@RestControllerAdvice
public class DbErrorHandler {

    @ExceptionHandler(DataAccessException.class)
    public ResponseEntity<Map<String, String>> dbError(DataAccessException e) {
        return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE).body(Map.of("error", Retry.describe(e)));
    }
}

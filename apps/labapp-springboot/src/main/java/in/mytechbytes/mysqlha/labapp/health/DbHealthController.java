package in.mytechbytes.mysqlha.labapp.health;

import java.util.Map;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class DbHealthController {
    private final DbProbe probe;
    public DbHealthController(DbProbe probe) { this.probe = probe; }

    /** curl localhost:8081/health/db   -> 200 when both paths are up, 503 otherwise */
    @GetMapping("/health/db")
    public ResponseEntity<Map<String, Object>> db() {
        Map<String, Object> s = probe.snapshot();
        boolean up = s.values().stream().filter(v -> v instanceof Map)
                .allMatch(v -> Boolean.TRUE.equals(((Map<?, ?>) v).get("up")));
        return ResponseEntity.status(up ? 200 : 503).body(s);
    }
}

package in.mytechbytes.mysqlha.labapp.orders;

import in.mytechbytes.mysqlha.labapp.configs.Retry;
import org.springframework.dao.DataAccessException;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.*;

import java.util.Map;

@RestController
@RequestMapping("/orders")
public class OrderController {
    private final OrderService orders;

    public OrderController(OrderService orders) {
        this.orders = orders;
    }

    @PostMapping
    public ResponseEntity<OrderService.WriteResult> create(
            @RequestParam(name = "item", defaultValue = "widget") String item,
            @RequestParam(name = "qty", defaultValue = "1") int qty,
            @RequestHeader(name = "Idempotency-Key", required = false) String key) {
        OrderService.WriteResult w = orders.create(item, qty, key);
        return ResponseEntity.status(w.deduplicated() ? HttpStatus.OK : HttpStatus.CREATED).body(w);
    }

    @GetMapping("/{id}")
    public ResponseEntity<?> get(@PathVariable("id") long id,
                                 @RequestParam(name = "fresh", defaultValue = "false") boolean fresh) {
        var r = orders.find(id, fresh);
        return r.value().<ResponseEntity<?>>map(ResponseEntity::ok)
                .orElseGet(() -> notFound(id, r.attempts()));
    }

    @GetMapping
    public OrderService.ReadResult<?> recent(@RequestParam(name = "limit", defaultValue = "10") int limit) {
        return orders.recent(limit);
    }

    @PutMapping("/{id}")
    public ResponseEntity<?> update(@PathVariable("id") long id, @RequestParam(name = "qty") int qty) {
        var r = orders.updateQty(id, qty);
        return r.value().<ResponseEntity<?>>map(ResponseEntity::ok)
                .orElseGet(() -> notFound(id, r.attempts()));
    }

    /** curl -X DELETE localhost:8081/orders/1 */
    @DeleteMapping("/{id}")
    public ResponseEntity<?> delete(@PathVariable("id") long id) {
        var r = orders.delete(id);
        return r.value() ? ResponseEntity.noContent().build() : notFound(id, r.attempts());
    }

    /** Errors that survived all retries -> 503 with the MySQL error text. */
    @ExceptionHandler(DataAccessException.class)
    public ResponseEntity<Map<String, String>> dbError(DataAccessException e) {
        return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE).body(Map.of("error", Retry.describe(e)));
    }

    private static ResponseEntity<Map<String, Object>> notFound(long id, int attempts) {
        return ResponseEntity.status(HttpStatus.NOT_FOUND)
                .body(Map.of("error", "order " + id + " not found", "attempts", attempts));
    }

}

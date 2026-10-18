package in.mytechbytes.mysqlha.labapp.orders;

import in.mytechbytes.mysqlha.labapp.configs.Retry;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.dao.DuplicateKeyException;
import org.springframework.jdbc.core.RowMapper;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Service;
import org.springframework.transaction.support.TransactionTemplate;

import java.util.List;
import java.util.Optional;
import java.util.UUID;

@Service
public class OrderService {
    public record WriteResult(Order order, int attempts, boolean deduplicated) {}
    public record ReadResult<T>(T value, int attempts) {}

    private static final String COLUMNS =
            "SELECT id, request_id, item, qty, written_on, created_at, updated_at, @@hostname AS read_from FROM orders ";

    private static final RowMapper<Order> ROW = (rs, n) -> new Order(
            rs.getLong("id"),
            rs.getString("request_id"),
            rs.getString("item"),
            rs.getInt("qty"),
            rs.getString("written_on"),
            rs.getTimestamp("created_at").toLocalDateTime(),
            rs.getTimestamp("updated_at").toLocalDateTime(),
            rs.getString("read_from")
    );

    private final JdbcClient jdbcClient;
    private final TransactionTemplate writeTx;
    private final TransactionTemplate readTx;
    private final Retry retry;

    public OrderService(JdbcClient jdbcClient, @Qualifier("writeTx") TransactionTemplate writeTx, @Qualifier("writeTx") TransactionTemplate readTx, Retry retry) {
        this.jdbcClient = jdbcClient;
        this.writeTx = writeTx;
        this.readTx = readTx;
        this.retry = retry;
    }

    public WriteResult create(String item, int qty, String requestIdOrNull) {
        String requestId = requestIdOrNull != null ? requestIdOrNull : UUID.randomUUID().toString();
        boolean[] dedup = {false};
        Retry.Result<Order> r = retry.run(Retry.Kind.WRITE, "create", () -> writeTx.execute(s -> {
            try {
                jdbcClient.sql("INSERT INTO orders (request_id, item, qty, written_on) VALUES (:rid, :item, :qty, @@hostname)")
                        .param("rid", requestId)
                        .param("item", item)
                        .param("qty", qty)
                        .update();
            } catch (DuplicateKeyException alreadyThere) {
                dedup[0] = true;
            }
            return jdbcClient.sql(COLUMNS + "WHERE request_id = :rid").param("rid", requestId).query(ROW).single();
        }));
        return new WriteResult(r.value(), r.attempts(), dedup[0]);
    }

    public ReadResult<Optional<Order>> find(long id, boolean fresh) {
        TransactionTemplate tx = fresh ? writeTx : readTx;
        Retry.Result<Optional<Order>> r = retry.run(Retry.Kind.READ, "read", () -> tx.execute(s ->
                jdbcClient.sql(COLUMNS + "WHERE id = :id").param("id", id).query(ROW).optional()));
        return new ReadResult<>(r.value(), r.attempts());
    }

    public ReadResult<List<Order>> recent(int limit) {
        int n = Math.clamp(limit, 1, 100);
        Retry.Result<List<Order>> r = retry.run(Retry.Kind.READ, "list", () -> readTx.execute(s ->
                jdbcClient.sql(COLUMNS + "ORDER BY id DESC LIMIT :n").param("n", n).query(ROW).list()));
        return new ReadResult<>(r.value(), r.attempts());
    }

    public ReadResult<Optional<Order>> updateQty(long id, int qty) {
        Retry.Result<Optional<Order>> r = retry.run(Retry.Kind.WRITE, "update", () -> writeTx.execute(s -> {
            jdbcClient.sql("UPDATE orders SET qty = :qty WHERE id = :id").param("qty", qty).param("id", id).update();
            return jdbcClient.sql(COLUMNS + "WHERE id = :id").param("id", id).query(ROW).optional();
        }));
        return new ReadResult<>(r.value(), r.attempts());
    }

    public ReadResult<Boolean> delete(long id) {
        Retry.Result<Integer> r = retry.run(Retry.Kind.WRITE, "delete", () -> writeTx.execute(s ->
                jdbcClient.sql("DELETE FROM orders WHERE id = :id").param("id", id).update()));
        return new ReadResult<>(r.value() > 0, r.attempts());
    }
}

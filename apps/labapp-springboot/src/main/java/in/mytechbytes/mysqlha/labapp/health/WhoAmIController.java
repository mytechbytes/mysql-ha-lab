package in.mytechbytes.mysqlha.labapp.health;

import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.transaction.support.TransactionSynchronizationManager;
import org.springframework.transaction.support.TransactionTemplate;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.LinkedHashMap;
import java.util.Map;

@RestController
public class WhoAmIController {
    private final JdbcClient jdbcClient;
    private final TransactionTemplate writeTx;
    private final TransactionTemplate readTx;

    public WhoAmIController(JdbcClient jdbcClient, @Qualifier("writeTx") TransactionTemplate writeTx, @Qualifier("readTx") TransactionTemplate readTx) {
        this.jdbcClient = jdbcClient;
        this.writeTx = writeTx;
        this.readTx = readTx;
    }

    @GetMapping("/whoami")
    public Map<String, Object> whoami() {
        Map<String, Object> result = new LinkedHashMap<>();
        result.put("writer", writeTx.execute(s -> probe()));
        result.put("reader", readTx.execute(s -> probe()));
        return result;
    }

    private Map<String, Object> probe() {
        Map<String, Object> row = jdbcClient.sql("SELECT @@hostname AS node, @@super_read_only AS read_only").query().singleRow();
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("pool", TransactionSynchronizationManager.isCurrentTransactionReadOnly() ? "reader" : "writer");
        out.putAll(row);
        return out;
    }
}

package in.mytechbytes.mysqlha.labapp.orders;

import in.mytechbytes.mysqlha.labapp.db.Retry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.ApplicationArguments;
import org.springframework.boot.ApplicationRunner;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Component;
import org.springframework.transaction.support.TransactionTemplate;

@Component
public class SchemaInit implements ApplicationRunner {
    private static final Logger log = LoggerFactory.getLogger(SchemaInit.class);

    private static final String DDL = """
            CREATE TABLE IF NOT EXISTS orders (
              id          BIGINT AUTO_INCREMENT PRIMARY KEY,
              request_id  CHAR(36)     NOT NULL,
              item        VARCHAR(64)  NOT NULL,
              qty         INT          NOT NULL,
              written_on  VARCHAR(64)  NOT NULL,
              created_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
              updated_at  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
              UNIQUE KEY uk_request_id (request_id),
              KEY ix_created_at (created_at)
            )""";

    private static final String HEARTBEAT_DDL = """
        CREATE TABLE IF NOT EXISTS heartbeat (
          id       TINYINT PRIMARY KEY,
          beat_at  TIMESTAMP(3) NOT NULL,
          node     VARCHAR(64)  NOT NULL
        )""";

    private final JdbcClient jdbc;
    private final TransactionTemplate writeTx;
    private final Retry retry;

    public SchemaInit(JdbcClient jdbc, @Qualifier("writeTx") TransactionTemplate writeTx, Retry retry) {
        this.jdbc = jdbc;
        this.writeTx = writeTx;
        this.retry = retry;
    }

    @Override
    public void run(ApplicationArguments args) throws Exception {
        retry.run(Retry.Kind.WRITE, "schema", () -> writeTx.execute(s -> jdbc.sql(DDL).update()));
        log.info("table orders ready");
        retry.run(Retry.Kind.WRITE, "schema", () -> writeTx.execute(s -> jdbc.sql(HEARTBEAT_DDL).update()));
        log.info("table heartbeat ready");
    }
}

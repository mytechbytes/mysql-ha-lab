package in.mytechbytes.mysqlha.labapp.db;

import com.zaxxer.hikari.HikariDataSource;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.jdbc.datasource.DataSourceTransactionManager;
import org.springframework.jdbc.datasource.LazyConnectionDataSourceProxy;
import org.springframework.transaction.PlatformTransactionManager;
import org.springframework.transaction.support.TransactionTemplate;

import javax.sql.DataSource;
import java.util.Map;

@Configuration
public class DataSourceConfig {
    @Bean(destroyMethod = "close")
    @ConfigurationProperties("app.datasource.writer")
    public HikariDataSource writerDataSource() {
        return new HikariDataSource();      // settings come from the YAML, pool starts on first use
    }

    @Bean(destroyMethod = "close")
    @ConfigurationProperties("app.datasource.reader")
    public HikariDataSource readerDataSource() {
        return new HikariDataSource();
    }

    @Bean
    @Primary
    public DataSource dataSource(@Qualifier("writerDataSource") DataSource writer,  @Qualifier("readerDataSource") DataSource reader) {
        ReadWriteRoutingDataSource routingDataSource = new ReadWriteRoutingDataSource();
        routingDataSource.setTargetDataSources(Map.of(
                ReadWriteRoutingDataSource.READER, reader,
                ReadWriteRoutingDataSource.WRITER, writer
        ));
        routingDataSource.setDefaultTargetDataSource(writer);
        routingDataSource.afterPropertiesSet();
        return new LazyConnectionDataSourceProxy(routingDataSource);
    }

    @Bean
    public PlatformTransactionManager transactionManager(@Qualifier("dataSource") DataSource dataSource) {
        return new DataSourceTransactionManager(dataSource);
    }

    @Bean
    public JdbcClient jdbcClient(@Qualifier("dataSource") DataSource dataSource) {
        return JdbcClient.create(dataSource);
    }

    @Bean
    public TransactionTemplate writeTx(PlatformTransactionManager transactionManager, @Value("${app.tx.timeout-seconds:10}") int timeoutSeconds) {
        TransactionTemplate template = new TransactionTemplate(transactionManager);
        template.setTimeout(timeoutSeconds);
        return template;
    }

    @Bean
    public TransactionTemplate readTx(PlatformTransactionManager transactionManager,  @Value("${app.tx.timeout-seconds:10}") int timeoutSeconds) {
        TransactionTemplate template = new TransactionTemplate(transactionManager);
        template.setReadOnly(true);
        template.setTimeout(timeoutSeconds);
        return template;
    }
}

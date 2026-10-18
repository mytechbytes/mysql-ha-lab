package in.mytechbytes.mysqlha.labapp.configs;

import org.jspecify.annotations.Nullable;
import org.springframework.jdbc.datasource.lookup.AbstractRoutingDataSource;
import org.springframework.transaction.support.TransactionSynchronizationManager;

public class ReadWriteRoutingDataSource extends AbstractRoutingDataSource {
    public static final String WRITER = "writer";
    public static final String READER = "reader";

    @Override
    protected @Nullable Object determineCurrentLookupKey() {
        return TransactionSynchronizationManager.isCurrentTransactionReadOnly() ? READER : WRITER;
    }
}

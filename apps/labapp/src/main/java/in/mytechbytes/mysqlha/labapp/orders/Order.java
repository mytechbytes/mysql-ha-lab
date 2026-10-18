package in.mytechbytes.mysqlha.labapp.orders;

import java.time.LocalDateTime;

public record Order(
        long id,
        String requestId,
        String item,
        int qty,
        String writtenOn,
        LocalDateTime createdAt,
        LocalDateTime updatedAt,
        String readFrom
) {}

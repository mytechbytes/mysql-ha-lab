package in.mytechbytes.mysqlha.labapp;

import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ActiveProfiles;

/**
 * Integration test: starts the whole app against the running cluster.
 * Uses the "dev" profile (localhost:6446 / 6447 and ../../.env), because the default
 * profile's host "mysql-router" only resolves inside the Docker network.
 *
 * Cluster not running? Build without tests: mvn package -DskipTests
 */
@SpringBootTest
@ActiveProfiles("dev")
class LabappApplicationTests {

	@Test
	void contextLoads() {
	}

}

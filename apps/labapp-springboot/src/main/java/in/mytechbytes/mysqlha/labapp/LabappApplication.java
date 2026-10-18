package in.mytechbytes.mysqlha.labapp;

import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.scheduling.annotation.EnableScheduling;

@SpringBootApplication
@EnableScheduling
public class LabappApplication {

	public static void main(String[] args) {
		SpringApplication.run(LabappApplication.class, args);
	}

}

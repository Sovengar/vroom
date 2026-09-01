package com.example.orders;

import java.util.Map;
import org.springframework.boot.SpringApplication;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@SpringBootApplication
public class OrdersApplication {

    public static void main(String[] args) {
        SpringApplication.run(OrdersApplication.class, args);
    }

    @RestController
    static class OrdersController {

        @GetMapping("/")
        Map<String, String> orders() {
            return Map.of("service", "orders-api-springboot", "status", "ok");
        }
    }
}

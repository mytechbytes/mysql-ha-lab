package in.mytechbytes.mysqlha.labapp.orders;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.Map;

@RestController
@RequestMapping("/load")
public class OrderLoadController {

    private final OrderLoadGenerator load;

    public OrderLoadController(OrderLoadGenerator load) { this.load = load; }

    @PostMapping("/start")  public Map<String, Object> start()  { load.start(); return load.status(); }
    @PostMapping("/stop")   public Map<String, Object> stop()   { load.stop();  return load.status(); }
    @PostMapping("/reset")  public Map<String, Object> reset()  { load.reset(); return load.status(); }
    @GetMapping("/status")  public Map<String, Object> status() { return load.status(); }
}

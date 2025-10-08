import uasyncio as asyncio
from machine import WDT, Pin
import network
import utime
import json
import binascii
import aiorepl

WIEGAND_D0_PIN = 14
WIEGAND_D1_PIN = 27
DOOR_PIN = 25
HTTP_TIMEOUT_SECONDS = 2
POLLING_INTERVAL_SECONDS = 10
CACHE_FILE = "conwaystate.json"

with open("network.conf", "r") as f:
    NETWORK_CFG = json.load(f)


def write_json(fname, data):
    s = json.dumps(data)
    crc = binascii.crc32(s.encode()) & 0xFFFFFFFF
    obj = {"_crc32": crc, "data": data}
    with open(fname, "w") as f:
        f.write(json.dumps(obj))


def read_json(fname):
    with open(fname) as f:
        obj = json.load(f)
    s = json.dumps(obj["data"])
    crc = binascii.crc32(s.encode()) & 0xFFFFFFFF
    if crc != obj.get("_crc32"):
        raise ValueError("CRC mismatch")
    return obj["data"]


async def wifi_loop():
    net = network.WLAN(network.WLAN.IF_STA)
    net.active(True)

    while True:
        if not net.isconnected():
            print("connecting to wifi...")
            net.connect(NETWORK_CFG["ssid"], NETWORK_CFG["password"])

            for _ in range(20):
                if net.isconnected():
                    print("wifi connected:", net.ifconfig())
                    break
                await asyncio.sleep(0.5)

            if not net.isconnected():
                print("wifi connection failed, retrying in 5s...")
                await asyncio.sleep(5)

        await asyncio.sleep(3)


class ConwayClient:
    def __init__(self):
        self._etag = ""
        self._fobs = []
        self._events = []
        self._warm_cache()

    def check_fob(self, fob: int) -> bool:
        return fob in self._fobs

    def push_event(self, fob: int, allowed: bool):
        self._events.append({"fob": fob, "allowed": allowed})
        if len(self._events) > 20:
            self._events.pop(0)

    async def run_polling_loop(self):
        while True:
            await asyncio.sleep(POLLING_INTERVAL_SECONDS)
            await self.sync()

    async def sync(self):
        try:
            await self._roundtrip()
        except Exception as e:
            print(f"error while attempting to sync with Conway: {e}")

    async def _roundtrip(self):
        # Build the request
        body = json.dumps(self._events)
        req_headers = {
            "Host": NETWORK_CFG["conwayHost"],
            "Content-Type": "application/json",
            "Content-Length": str(len(body)),
            "If-None-Match": self._etag,
            "Connection": "close",
        }
        header_str = "".join(f"{k}: {v}\r\n" for k, v in req_headers.items())
        request = f"POST /api/fobs HTTP/1.1\r\n{header_str}\r\n{body}"

        # Roundtrip to the server
        reader, writer = await asyncio.open_connection(NETWORK_CFG["conwayHost"], NETWORK_CFG["conwayPort"])
        writer.write(request.encode())
        await writer.drain()
        response_data = await asyncio.wait_for(reader.read(-1), HTTP_TIMEOUT_SECONDS)
        writer.close()
        await writer.wait_closed()

        # Parse the response
        header_blob, _, body_blob = response_data.partition(b"\r\n\r\n")
        header_lines = header_blob.decode().split("\r\n")
        _, status, _ = header_lines[0].split(" ", 2)
        status = int(status)

        if status == 304:
            self._events.clear()
            return

        if status != 200:
            raise Exception(f"Unexpected server response status: {status}")

        # Find the etag in the response headers
        for line in header_lines[1:]:
            if ":" in line:
                k, v = line.split(":", 1)
                if k == "Etag":
                    self._etag = v

        # Cache the response
        self._fobs = json.loads(body_blob.decode())
        self._events.clear()
        await self._flush_cache()
        print("sync'd with conway server")

    def _warm_cache(self):
        try:
            data = read_json(CACHE_FILE)
            self._fobs = data.get("fobs", [])
            self._etag = data.get("etag", "")
            print(f"loaded {len(self._fobs)} fobs from flash")
        except Exception as e:
            print(f"unable to load filesystem cache: {e}")

    async def _flush_cache(self):
        try:
            data = {"etag": self._etag, "fobs": self._fobs}
            write_json(CACHE_FILE, data)
        except Exception as e:
            print(f"unable to write filesystem cache: {e}")


class Wiegand:
    CARD_MASK = (1 << 17) - 2
    FACILITY_MASK = 0xFF << 17
    DEBOUNCE_US = 200
    END_OF_TX_US = 25000

    def __init__(self, pin0, pin1, callback):
        self.callback = callback
        self.last_card = None
        self.bit_buffer = []
        self.last_bit_time = None
        self.cards_read = 0

        self.pin0 = Pin(pin0, Pin.IN, Pin.PULL_UP)
        self.pin1 = Pin(pin1, Pin.IN, Pin.PULL_UP)
        self.pin0.irq(trigger=Pin.IRQ_FALLING, handler=lambda *_: self._on_pin(0))
        self.pin1.irq(trigger=Pin.IRQ_FALLING, handler=lambda *_: self._on_pin(1))

    def _on_pin(self, bit_value):
        now = utime.ticks_us()
        if self.last_bit_time and utime.ticks_diff(now, self.last_bit_time) < Wiegand.DEBOUNCE_US:
            return
        self.last_bit_time = now
        self.bit_buffer.append(bit_value)

    async def run(self):
        while True:
            await asyncio.sleep_ms(5)
            now = utime.ticks_us()
            if self.last_bit_time and self.bit_buffer and utime.ticks_diff(now, self.last_bit_time) >= Wiegand.END_OF_TX_US:
                value = int("".join(map(str, self.bit_buffer)), 2)

                if not self._check_parity(value):
                    print("parity check failed!")
                    self._reset()
                    continue

                self.last_card = value
                self.cards_read += 1
                card_id = (value & Wiegand.CARD_MASK) >> 1
                facility = (value & Wiegand.FACILITY_MASK) >> 17
                await self.callback(card_id, facility, self.cards_read)
                self._reset()

    def _check_parity(self, value):
        leading, trailing = (value >> 25) & 1, value & 1
        data = (value >> 1) & ((1 << 24) - 1)
        return (bin(data >> 12).count("1") % 2) == leading and (bin(data & 0xFFF).count("1") % 2) != trailing

    def _reset(self):
        self.bit_buffer.clear()
        self.last_bit_time = None


class Server:
    def __init__(self, mainloop):
        self._main = mainloop

    async def run(self):
        await asyncio.start_server(lambda r, w: self._accept(r, w), "0.0.0.0", 80)

    async def _accept(self, reader, writer):
        try:
            request_line = await reader.readline()
            if not request_line:
                await writer.aclose()
                return

            # Read the request
            method, path, _ = request_line.decode().split()
            while True:
                line = await reader.readline()
                if line == b"\r\n" or not line:
                    break

            response_body = """
                <form action="/unlock" method="post">
                    <button type="submit">Unlock</button>
                </form>
            """
            if method == "POST" and path == "/unlock":
                await self._main.open_door()
                response_body = "<p>Door unlocked!</p>"

            response = (
                "HTTP/1.1 200 OK\r\n"
                "Content-Type: text/html\r\n"
                f"Content-Length: {len(response_body)}\r\n"
                "Connection: close\r\n"
                "\r\n"
                f"{response_body}"
            )
            await writer.awrite(response)

        except Exception as e:
            print("HTTP server error:", e)
        finally:
            await writer.aclose()


class MainLoop:
    def __init__(self, conway):
        self.conway = conway
        self.door = Pin(DOOR_PIN, Pin.OUT)
        self._failed_attempts = 0
        self._backoff_until = 0

    async def on_card(self, card_number, facility_code, _):
        now = utime.ticks_ms()
        if now < self._backoff_until:
            print("ignoring card read due to backoff")
            return

        combined = int(f"{facility_code}{card_number}")
        print(f"saw card {combined}")

        allowed = self.conway.check_fob(combined)
        if not allowed:
            await self.conway.sync()
            allowed = self.conway.check_fob(combined)

        self.conway.push_event(combined, allowed)

        if allowed:
            print("access granted")
            self._failed_attempts = 0
            asyncio.create_task(self.open_door())
        else:
            print("access denied")
            self._failed_attempts += 1
            delay = min(8000, (1 << self._failed_attempts) * 1000)
            self._backoff_until = now + delay

    async def open_door(self):
        print("opening door")
        self.door.on()
        await asyncio.sleep(0.2)
        self.door.off()

    async def run_watchdog(self):
        wdt = WDT(timeout=30000)
        while True:
            # Just make sure the asyncio loop isn't blocked
            await asyncio.sleep(20)
            wdt.feed()


conway = ConwayClient()
main = MainLoop(conway)
wiegand = Wiegand(WIEGAND_D0_PIN, WIEGAND_D1_PIN, main.on_card)
svr = Server(main)


async def aio_main():
    await asyncio.gather(
        wifi_loop(),
        conway.run_polling_loop(),
        svr.run(),
        wiegand.run(),
        main.run_watchdog(),
        aiorepl.task(),
    )


asyncio.run(aio_main())

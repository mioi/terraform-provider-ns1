variable "ns1_apikey" {
  default = "yjN2AxbL8CNrfRdxk4Qs"
}

provider "ns1" {
  apikey = "${var.ns1_apikey}"
}

resource "ns1_record" "rec" {
  domain = "test1.serverless.farm"
  zone   = "serverless.farm"
  type   = "A"
  ttl    = 120

  lifecycle {
    ignore_changes = ["regions", "answers"]
  }
}

resource "ns1_answer" "ble" {
  record = "${ns1_record.rec.domain}"
  answer = "10.3.0.6"

  meta = [
    {
      up = "1"
    },
  ]
}

resource "ns1_answer" "ble2" {
  record = "${ns1_record.rec.domain}"
  answer = "10.4.0.6"

  meta = [
    {
      up = "0"
    },
  ]
}

resource "ns1_answer" "one" {
  record = "test.serverless.farm"
  answer = "10.2.2.14"
  region = "us-west"

  meta = [
    {
      weight = "51"
      up     = "0"
    },
  ]
}

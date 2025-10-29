# Exports the last 3 years of Paypal transactions to a csv file

import os
import csv
import requests
from datetime import datetime, timedelta
from typing import List, Dict


class PayPalAPI:
    def __init__(self, client_id: str, secret: str):
        self.client_id = client_id
        self.secret = secret
        self.base_url = "https://api.paypal.com"
        self.access_token = None

    def authenticate(self) -> str:
        """Get OAuth access token."""
        url = f"{self.base_url}/v1/oauth2/token"
        headers = {
            "Accept": "application/json",
            "Accept-Language": "en_US",
        }
        data = {"grant_type": "client_credentials"}

        response = requests.post(
            url, headers=headers, data=data, auth=(self.client_id, self.secret)
        )

        if response.status_code == 200:
            self.access_token = response.json()["access_token"]
            return self.access_token
        else:
            raise Exception(f"Authentication failed: {response.text}")

    def get_transactions(self, start_date: str, end_date: str) -> List[Dict]:
        if not self.access_token:
            self.authenticate()

        start_dt = datetime.fromisoformat(start_date.replace("Z", "+00:00"))
        end_dt = datetime.fromisoformat(end_date.replace("Z", "+00:00"))
        all_transactions = []
        current_start = start_dt

        while current_start < end_dt:
            current_end = min(current_start + timedelta(days=31), end_dt)
            chunk_start = current_start.strftime("%Y-%m-%dT%H:%M:%SZ")
            chunk_end = current_end.strftime("%Y-%m-%dT%H:%M:%SZ")

            print(f"Fetching transactions from {chunk_start} to {chunk_end}")
            chunk_transactions = self._fetch_transaction_chunk(chunk_start, chunk_end)
            all_transactions.extend(chunk_transactions)
            current_start = current_end + timedelta(seconds=1)

        return all_transactions

    def _fetch_transaction_chunk(self, start_date: str, end_date: str) -> List[Dict]:
        url = f"{self.base_url}/v1/reporting/transactions"
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {self.access_token}",
        }

        params = {
            "start_date": start_date,
            "end_date": end_date,
            "fields": "all",
            "page_size": 500,  # Max allowed
        }

        chunk_transactions = []
        page = 1

        while True:
            params["page"] = page
            response = requests.get(url, headers=headers, params=params)

            if response.status_code != 200:
                raise Exception(f"Failed to fetch transactions: {response.text}")

            data = response.json()
            transactions = data.get("transaction_details", [])

            if not transactions:
                break

            chunk_transactions.extend(transactions)
            print(f"  Page {page}: {len(transactions)} transactions")

            # Check if there are more pages
            if len(transactions) < params["page_size"]:
                break

            page += 1

        print(f"  Total for this chunk: {len(chunk_transactions)} transactions")
        return chunk_transactions


def format_transaction_for_csv(transaction: Dict) -> Dict:
    transaction_info = transaction.get("transaction_info", {})
    payer_info = transaction.get("payer_info", {})
    cart_info = transaction.get("cart_info", {})

    return {
        "transaction_id": transaction_info.get("transaction_id", ""),
        "subscription_id": transaction_info.get("billing_agreement_id", ""),
        "transaction_date": transaction_info.get("transaction_initiation_date", ""),
        "transaction_status": transaction_info.get("transaction_status", ""),
        "transaction_note": transaction_info.get("transaction_note", ""),
        "transaction_amount": transaction_info.get("transaction_amount", {}).get(
            "value", ""
        ),
        "fee_amount": transaction_info.get("fee_amount", {}).get("value", ""),
        "payer_email": payer_info.get("email_address", ""),
        "payer_name": payer_info.get("payer_name", {}).get("alternate_full_name", ""),
        "item_name": (
            cart_info.get("item_details", [{}])[0].get("item_name", "")
            if cart_info.get("item_details")
            else ""
        ),
        "item_quantity": (
            cart_info.get("item_details", [{}])[0].get("item_quantity", "")
            if cart_info.get("item_details")
            else ""
        ),
    }


def export_to_csv(transactions: List[Dict], filename: str):
    if not transactions:
        print("No transactions to export")
        return

    formatted_transactions = [format_transaction_for_csv(t) for t in transactions]
    fieldnames = list(formatted_transactions[0].keys())

    with open(filename, "w", newline="", encoding="utf-8") as csvfile:
        writer = csv.DictWriter(csvfile, fieldnames=fieldnames)
        writer.writeheader()
        writer.writerows(formatted_transactions)

    print(f"Exported {len(formatted_transactions)} transactions to {filename}")


def main():
    client_id = os.getenv("PAYPAL_CLIENT_ID")
    secret = os.getenv("PAYPAL_SECRET")

    if not client_id or not secret:
        print(
            "Error: PAYPAL_CLIENT_ID and PAYPAL_SECRET environment variables must be set"
        )
        print("\nUsage:")
        print('  export PAYPAL_CLIENT_ID="your_client_id"')
        print('  export PAYPAL_SECRET="your_secret"')
        print("  python export_transactions.py")
        return

    end_date = datetime.now()
    start_date = end_date - timedelta(days=(365 * 3) - 1)  # paypal api has max of 3yr
    start_date_str = start_date.strftime("%Y-%m-%dT00:00:00Z")
    end_date_str = end_date.strftime("%Y-%m-%dT23:59:59Z")

    print(f"Fetching PayPal transactions from {start_date_str} to {end_date_str}")

    try:
        paypal = PayPalAPI(client_id, secret)
        paypal.authenticate()
        print("Fetching transactions...")
        transactions = paypal.get_transactions(start_date_str, end_date_str)

        print(f"Total transactions fetched: {len(transactions)}")

        output_filename = f'paypal_transactions_{start_date.strftime("%Y%m%d")}_{end_date.strftime("%Y%m%d")}.csv'
        export_to_csv(transactions, output_filename)

    except Exception as e:
        print(f"Error: {e}")
        return 1

    return 0


if __name__ == "__main__":
    exit(main())

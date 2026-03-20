#!/usr/bin/env python3
"""
Seidr v2 Health Verification Script

Verifies Seidr v2 is healthy by checking:
1. /health endpoint
2. /query endpoint (vector search)
3. /cognitive/status endpoint

Prints a summary of the results.
"""

import http.client
import json
import sys

SEIDR_HOST = "seidr.valhalla.svc.cluster.local"
SEIDR_PORT = 8082

def check_health() -> dict:
    """Check the /health endpoint."""
    conn = http.client.HTTPConnection(SEIDR_HOST, SEIDR_PORT, timeout=10)
    try:
        conn.request("GET", "/health")
        response = conn.getresponse()
        data = response.read().decode("utf-8")
        status = response.status
        conn.close()
        
        if status == 200:
            result = json.loads(data)
            return {"status": "OK", "data": result, "code": status}
        else:
            return {"status": "FAILED", "error": f"HTTP {status}", "code": status}
    except Exception as e:
        return {"status": "FAILED", "error": str(e), "code": None}


def check_query() -> dict:
    """Test vector search with /query endpoint."""
    conn = http.client.HTTPConnection(SEIDR_HOST, SEIDR_PORT, timeout=10)
    try:
        payload = json.dumps({"agent": "val", "query": "test", "n_results": 1})
        headers = {"Content-Type": "application/json"}
        conn.request("POST", "/query", body=payload, headers=headers)
        response = conn.getresponse()
        data = response.read().decode("utf-8")
        status = response.status
        conn.close()
        
        if status == 200:
            result = json.loads(data)
            count = result.get("count", 0)
            return {"status": "OK", "data": result, "code": status, "results_found": count}
        else:
            return {"status": "FAILED", "error": f"HTTP {status}", "code": status}
    except Exception as e:
        return {"status": "FAILED", "error": str(e), "code": None}


def check_cognitive_status() -> dict:
    """Check /cognitive/status endpoint."""
    conn = http.client.HTTPConnection(SEIDR_HOST, SEIDR_PORT, timeout=10)
    try:
        conn.request("GET", "/cognitive/status")
        response = conn.getresponse()
        data = response.read().decode("utf-8")
        status = response.status
        conn.close()
        
        if status == 200:
            result = json.loads(data)
            return {"status": "OK", "data": result, "code": status}
        else:
            return {"status": "FAILED", "error": f"HTTP {status}", "code": status}
    except Exception as e:
        return {"status": "FAILED", "error": str(e), "code": None}


def print_summary(results: dict) -> int:
    """Print a summary of all checks and return pass count."""
    passed = 0
    failed = 0
    
    print("\n" + "=" * 60)
    print("SEIDR v2 HEALTH CHECK SUMMARY")
    print("=" * 60 + "\n")
    
    for check_name, result in results.items():
        print(f"Check: {check_name}")
        print("-" * 40)
        
        if result["status"] == "OK":
            passed += 1
            print(f"  Status: PASSED")
            print(f"  HTTP Code: {result['code']}")
            
            if "data" in result:
                data = result["data"]
                if check_name == "health":
                    memories = data.get("memories", 0)
                    collections = len(data.get("collections", []))
                    print(f"  Memories: {memories}")
                    print(f"  Collections: {collections}")
                elif check_name == "query":
                    results_count = result.get("results_found", 0)
                    print(f"  Results returned: {results_count}")
                elif check_name == "cognitive_status":
                    enabled = data.get("enabled", False)
                    total_facts = data.get("stats", {}).get("total_facts_extracted", 0)
                    contradictions = data.get("stats", {}).get("contradictions_detected", 0)
                    print(f"  Cognitive enabled: {enabled}")
                    print(f"  Facts extracted: {total_facts}")
                    print(f"  Contradictions detected: {contradictions}")
        else:
            failed += 1
            print(f"  Status: FAILED")
            print(f"  Error: {result.get('error', 'Unknown')}")
        
        print()
    
    print("=" * 60)
    print(f"RESULTS: {passed} passed, {failed} failed")
    print("=" * 60 + "\n")
    
    return passed


def main():
    """Run all health checks and print summary."""
    print("Starting Seidr v2 health verification...")
    
    results = {
        "health": check_health(),
        "query": check_query(),
        "cognitive_status": check_cognitive_status(),
    }
    
    passed = print_summary(results)
    
    # Exit with error code if any checks failed
    sys.exit(0 if passed == 3 else 1)


if __name__ == "__main__":
    main()
# Status Condition Fix - Ready Condition Now Persists

## Problem

The `Ready` condition was being set in memory but **not persisting** to the Kubernetes API server. Users would see:
- Only `Synced` condition in status
- No `Ready` condition showing package availability
- Status not updating to `Deleting` when resource was deleted
- Conditions "flashing" and disappearing during installation

### Root Cause

The Crossplane managed reconciler **does not automatically persist status.conditions**. While it persists:
- `spec` changes (late initialization)  
- `status.atProvider` fields
- Its own `Synced` condition

It expects the external connector to **explicitly update** the status subresource after setting custom conditions like `Ready`.

## Solution

Added explicit `c.kube.Status().Update(ctx, cr)` calls after setting conditions in three key places:

### 1. **During Installation** (`handleNotInstalled`)
```go
cr.SetConditions(xpv1.Creating())
// NEW: Explicitly persist the Creating condition
if err := c.kube.Status().Update(ctx, cr); err != nil {
    c.logger.Info("Failed to update status with Creating condition", "error", err)
}
```

**Result**: Users now see `Ready=False, Reason=Creating` during deployment

### 2. **After Installation** (`handleInstalled`)
```go
cr.SetConditions(xpv1.Available())
// NEW: Explicitly persist the Available condition  
if err := c.kube.Status().Update(ctx, cr); err != nil {
    c.logger.Info("Failed to update status with Available condition", "error", err)
} else {
    c.logger.Info("Successfully updated status with Available condition", "packageName", packageName)
}
```

**Result**: Users now see `Ready=True, Reason=Available` when package is deployed

### 3. **During Deletion** (`observeDeletion`)
```go
cr.SetConditions(xpv1.Deleting())
// NEW: Explicitly persist the Deleting condition
if err := c.kube.Status().Update(ctx, cr); err != nil {
    c.logger.V(1).Info("Failed to update status with Deleting condition", "error", err)
}
```

**Result**: Users now see `Ready=False, Reason=Deleting` immediately when deletion starts

## Expected Status Progression

### Normal Deployment Flow
```yaml
# 1. Resource created (before deployment starts)
status:
  conditions:
    - type: Synced
      status: "True"
      reason: ReconcileSuccess

# 2. Deployment started (Create goroutine launched)  
status:
  atProvider:
    phase: Installing
  conditions:
    - type: Ready
      status: "False"
      reason: Creating
    - type: Synced
      status: "True"
      reason: ReconcileSuccess

# 3. Deployment complete (Observe detects installed package)
status:
  atProvider:
    phase: Installed
    packageName: podinfo-flux
  conditions:
    - type: Ready
      status: "True"
      reason: Available
    - type: Synced
      status: "True"
      reason: ReconcileSuccess
```

### Deletion Flow
```yaml
# 1. Deletion initiated (kubectl delete)
metadata:
  deletionTimestamp: "2025-10-01T17:25:59Z"
status:
  atProvider:
    phase: Removing
  conditions:
    - type: Ready
      status: "False"
      reason: Deleting
    - type: Synced
      status: "True"
      reason: ReconcileSuccess

# 2. Deletion complete (package removed from cluster)
# Resource is finalized and removed from API server
```

## Testing Verification

Deploy a test package and verify conditions:

```bash
# Deploy
kubectl apply -f deploy/04-test-zarfpackage.yaml

# Watch status progression
kubectl get zarfpackage podinfo-flux -o jsonpath='{.status.conditions}' -w

# Verify Ready condition appears
kubectl get zarfpackage podinfo-flux -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'

# Test deletion status
kubectl delete zarfpackage podinfo-flux
kubectl get zarfpackage podinfo-flux -o yaml  # Should show Deleting condition
```

## Key Changes

1. **Added explicit status updates** after `SetConditions()` calls
2. **Passed context through** to all helper functions for proper status updates
3. **Added debug logging** to track status update success/failure
4. **Fixed event emission logic** to only fire on transition to Available

## Benefits

✅ Users can see package readiness state via standard Kubernetes conventions  
✅ Status properly reflects `Creating → Available → Deleting` lifecycle  
✅ Compatible with Crossplane CLI (`kubectl crossplane beta trace`)  
✅ Works with standard kubectl commands (`kubectl wait --for=condition=Ready`)  
✅ Follows Crossplane best practices for managed resources

## Related Files Changed

- `internal/controller/zarfpackage/zarfpackage.go`:
  - `observeDeletion()` - Added status update after Deleting condition
  - `handleNotInstalled()` - Added status update after Creating condition
  - `handleInstalled()` - Added status update after Available condition

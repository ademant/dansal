# Issue: Extend Location Attributes with Floor/Ground Conditions

## Description
Extend the location model to include a new attribute for floor/ground conditions. This attribute should be overridable by events.

## Proposed Conditions
- wooden parquet
- stone floor
- grass
- tiles
- sand / gravel
- pavement

## Requirements
1. Add a new field `floor_condition` to the location model
2. Allow events to override the location's floor condition
3. Ensure backward compatibility with existing locations
4. Update any relevant UI/forms to include this new field
5. Consider database migration for existing locations

## Implementation Notes
- The field should be optional (nullable)
- Should follow the same override pattern as other location attributes
- Consider using an enum or predefined list for consistency

## Priority
Medium - Enhancement for better location description and accessibility information
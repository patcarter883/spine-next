; Exported API of a Python module: module-level functions, classes, public
; methods and UPPER_CASE constants. Names with a single leading underscore are
; private by convention; dunder names (e.g. __init__) are part of the API.
;
; Captures: @def.<kind> (whole definition), @name, @scope (container name),
; @sig (signature parts, concatenated), @import (imported module).

(module (function_definition
  name: (identifier) @name
  parameters: (parameters) @sig
  return_type: (_)? @sig) @def.function
  (#not-match? @name "^_[^_]"))

(module (decorated_definition definition: (function_definition
  name: (identifier) @name
  parameters: (parameters) @sig
  return_type: (_)? @sig)) @def.function
  (#not-match? @name "^_[^_]"))

(module (class_definition
  name: (identifier) @name
  superclasses: (argument_list)? @sig) @def.class
  (#not-match? @name "^_"))

(module (decorated_definition definition: (class_definition
  name: (identifier) @name
  superclasses: (argument_list)? @sig)) @def.class
  (#not-match? @name "^_"))

(module (class_definition
  name: (identifier) @scope
  body: (block (function_definition
    name: (identifier) @name
    parameters: (parameters) @sig
    return_type: (_)? @sig) @def.method))
  (#not-match? @scope "^_")
  (#not-match? @name "^_[^_]"))

(module (class_definition
  name: (identifier) @scope
  body: (block (decorated_definition definition: (function_definition
    name: (identifier) @name
    parameters: (parameters) @sig
    return_type: (_)? @sig)) @def.method))
  (#not-match? @scope "^_")
  (#not-match? @name "^_[^_]"))

(module (expression_statement (assignment
  left: (identifier) @name
  type: (_)? @sig)) @def.const
  (#match? @name "^[A-Z][A-Z0-9_]*$"))

(import_statement name: (dotted_name) @import)
(import_statement name: (aliased_import name: (dotted_name) @import))
(import_from_statement module_name: (_) @import)

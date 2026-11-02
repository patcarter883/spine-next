; Exported API of a TypeScript/JavaScript module (also used for TSX/JSX).
; Only declarations under an `export` statement count. Private/protected
; methods are filtered in code (accessibility_modifier).

(export_statement declaration: (function_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @sig
  return_type: (type_annotation)? @sig) @def.function)

(export_statement declaration: (generator_function_declaration
  name: (identifier) @name
  parameters: (formal_parameters) @sig
  return_type: (type_annotation)? @sig) @def.function)

(export_statement declaration: (class_declaration
  name: (type_identifier) @name
  (class_heritage)? @sig) @def.class)

(export_statement declaration: (abstract_class_declaration
  name: (type_identifier) @name
  (class_heritage)? @sig) @def.class)

(export_statement declaration: (class_declaration
  name: (type_identifier) @scope
  body: (class_body (method_definition
    name: (property_identifier) @name
    parameters: (formal_parameters) @sig
    return_type: (type_annotation)? @sig) @def.method)))

(export_statement declaration: (abstract_class_declaration
  name: (type_identifier) @scope
  body: (class_body [
    (method_definition
      name: (property_identifier) @name
      parameters: (formal_parameters) @sig
      return_type: (type_annotation)? @sig)
    (abstract_method_signature
      name: (property_identifier) @name
      parameters: (formal_parameters) @sig
      return_type: (type_annotation)? @sig)
  ] @def.method)))

(export_statement declaration: (interface_declaration
  name: (type_identifier) @name
  body: (_) @sig) @def.interface)

(export_statement declaration: (type_alias_declaration
  name: (type_identifier) @name
  value: (_) @sig) @def.type)

(export_statement declaration: (enum_declaration
  name: (identifier) @name
  body: (_) @sig) @def.enum)

(export_statement declaration: (lexical_declaration (variable_declarator
  name: (identifier) @name
  type: (type_annotation)? @sig) @def.const))

(import_statement source: (string (string_fragment) @import))
(export_statement source: (string (string_fragment) @import))
(call_expression
  function: (identifier) @_req
  arguments: (arguments . (string (string_fragment) @import))
  (#eq? @_req "require"))
